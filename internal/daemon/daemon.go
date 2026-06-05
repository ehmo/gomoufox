package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ehmo/gomoufox/internal/policy"
	"github.com/ehmo/gomoufox/internal/sidecar"
)

var (
	ErrAuthTokenRequired = errors.New("gomoufox serve requires --auth-token")
	ErrInvalidConfig     = errors.New("invalid daemon config")
)

type Config struct {
	Version            string
	AuthToken          string
	EnableEval         bool
	AllowSessionExport bool
	MaxInputBytes      int
	Ready              bool
	ActiveSessions     func() int
	Executor           Executor
}

type Executor func(context.Context, string, Envelope) Result

type Envelope struct {
	Args    []string       `json:"args"`
	Flags   map[string]any `json:"flags"`
	Profile string         `json:"profile,omitempty"`
	JSON    bool           `json:"json,omitempty"`
}

type Result struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

type Server struct {
	cfg Config
}

func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.AuthToken) == "" {
		return nil, ErrAuthTokenRequired
	}
	cap, err := policy.ClampInputCap(cfg.MaxInputBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	cfg.MaxInputBytes = cap
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if cfg.Executor == nil {
		cfg.Executor = func(context.Context, string, Envelope) Result {
			return Result{ExitCode: 1, Stderr: "command execution is not configured"}
		}
	}
	return &Server{cfg: cfg}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
		s.health(w)
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/commands/") {
		s.command(w, r)
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/session/") {
		s.sessionCommand(w, r)
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") {
		s.sessionLifecycle(w, r)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
}

func (s *Server) authorized(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	return strings.TrimPrefix(auth, "Bearer ") == s.cfg.AuthToken
}

func (s *Server) health(w http.ResponseWriter) {
	active := 0
	if s.cfg.ActiveSessions != nil {
		active = s.cfg.ActiveSessions()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":            s.cfg.Version,
		"ready":              s.cfg.Ready,
		"active_sessions":    active,
		"camoufox_pkg":       sidecar.RequiredCamoufox,
		"playwright":         sidecar.RequiredPlaywright,
		"camoufox_bin":       sidecar.CamoufoxBinaryVersion,
		"playwright_driver":  sidecar.PlaywrightGoVersion,
		"max_input_bytes":    s.cfg.MaxInputBytes,
		"eval_enabled":       s.cfg.EnableEval,
		"protocol_base_path": "/v1",
	})
}

func (s *Server) command(w http.ResponseWriter, r *http.Request) {
	verb := strings.TrimPrefix(r.URL.Path, "/v1/commands/")
	if !allowedCommand(verb) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		return
	}
	if verb == "eval" && !s.cfg.EnableEval {
		writeJSON(w, http.StatusForbidden, Result{ExitCode: 1, Stderr: "eval_disabled\n"})
		return
	}
	env, err := s.decodeEnvelope(w, r, false)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, Result{ExitCode: 2, Stderr: "invalid command envelope"})
		return
	}
	result := s.cfg.Executor(r.Context(), verb, env)
	writeJSON(w, http.StatusOK, redactResult(result))
}

func (s *Server) sessionCommand(w http.ResponseWriter, r *http.Request) {
	subcommand := strings.TrimPrefix(r.URL.Path, "/v1/session/")
	switch subcommand {
	case "export", "import":
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		return
	}
	if subcommand == "export" && !s.cfg.AllowSessionExport {
		writeJSON(w, http.StatusForbidden, Result{ExitCode: 2, Stderr: "session_export_disabled\n"})
		return
	}
	env, err := s.decodeEnvelope(w, r, false)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, Result{ExitCode: 2, Stderr: "invalid command envelope"})
		return
	}
	result := s.cfg.Executor(r.Context(), "session "+subcommand, env)
	writeJSON(w, http.StatusOK, redactResult(result))
}

func (s *Server) sessionLifecycle(w http.ResponseWriter, r *http.Request) {
	raw, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/v1/sessions/"), "/destroy")
	if !ok || raw == "" || strings.Contains(raw, "/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		return
	}
	sessionID, err := url.PathUnescape(raw)
	if err != nil || strings.TrimSpace(sessionID) == "" || strings.Contains(sessionID, "/") {
		writeJSON(w, http.StatusBadRequest, Result{ExitCode: 2, Stderr: "invalid session id"})
		return
	}
	env, err := s.decodeEnvelope(w, r, true)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, Result{ExitCode: 2, Stderr: "invalid command envelope"})
		return
	}
	env.Args = append([]string{sessionID}, env.Args...)
	result := s.cfg.Executor(r.Context(), "session destroy", env)
	writeJSON(w, http.StatusOK, redactResult(result))
}

func (s *Server) decodeEnvelope(w http.ResponseWriter, r *http.Request, allowEmpty bool) (Envelope, error) {
	var env Envelope
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, int64(s.cfg.MaxInputBytes)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return Envelope{}, nil
		}
		return Envelope{}, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Envelope{}, errors.New("invalid trailing envelope data")
	}
	return env, nil
}

func allowedCommand(verb string) bool {
	switch verb {
	case "get", "screenshot", "fetch", "eval":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func redactResult(result Result) Result {
	result.Stderr = policy.Redact(result.Stderr)
	return result
}
