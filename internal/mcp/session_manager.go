package mcp

import (
	"errors"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ehmo/gomoufox"
)

var (
	errSessionLimit        = errors.New("session limit reached")
	errSessionExists       = errors.New("session already exists")
	errSessionNotFound     = errors.New("session not found")
	errSessionOptions      = errors.New("session configuration differs")
	errSessionClosing      = errors.New("session is closing")
	errHARStopRequired     = errors.New("HAR stop required")
	errHARNotRecording     = errors.New("session is not recording HAR")
	errHARDestinationInUse = errors.New("HAR destination is already in use")
	sessionStoreGOOS       = runtime.GOOS
)

type profileOwnerError struct{ sessionID string }

func (e *profileOwnerError) Error() string { return "profile is owned by another session" }

type sessionCapacityError struct {
	used, limit int
	ids         []string
	omitted     int
}

func (*sessionCapacityError) Error() string { return errSessionLimit.Error() }
func (*sessionCapacityError) Unwrap() error { return errSessionLimit }

func (s *sessionStore) capacityErrorLocked() error {
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		if len(id) <= 128 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return &sessionCapacityError{used: len(s.sessions), limit: s.max, ids: ids, omitted: len(s.sessions) - len(ids)}
}

func (s *sessionStore) capacity() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]int{"used": len(s.sessions), "limit": s.max, "available": s.max - len(s.sessions)}
}

type sessionLifecycle uint32

const (
	sessionInitializing sessionLifecycle = iota
	sessionActive
	sessionClosing
	sessionClosed
)

type sessionStore struct {
	mu           sync.Mutex
	sessions     map[string]*sessionState
	harPaths     map[string]*sessionState
	profilePaths map[string]*sessionState
	max          int
	ttl          time.Duration
	now          func() time.Time
	closed       bool
}

type sessionState struct {
	width, height    int
	headful          bool
	id               string
	url              string
	proxy            string
	locale           string
	os               string
	profilePath      string
	storageStatePath string
	har              *harSessionOptions
	createdAt        time.Time
	lastUsed         time.Time
	opMu             sync.Mutex
	browser          browserSession
	harTimer         *time.Timer
	harResult        *gomoufox.HARResult
	lifecycle        atomic.Uint32
}

type harSessionOptions struct {
	path         string
	responsePath string
	capture      string
	urlFilter    string
	maxBytes     int64
	overwrite    bool
	duration     time.Duration
	deadline     time.Time
}

type sessionOptions struct {
	width, height    int
	headful          bool
	id               string
	proxy            string
	locale           string
	os               string
	profilePath      string
	storageStatePath string
	har              *harSessionOptions
}

func newSessionStore(max int, ttl time.Duration) *sessionStore {
	return &sessionStore{
		sessions:     map[string]*sessionState{},
		harPaths:     map[string]*sessionState{},
		profilePaths: map[string]*sessionState{},
		max:          max,
		ttl:          ttl,
		now:          time.Now,
	}
}

func (s *sessionStore) touch(id string, update func(*sessionState)) error {
	_, err := s.touchState(id, update)
	return err
}

func (s *sessionStore) touchState(id string, update func(*sessionState)) (*sessionState, error) {
	return s.touchStateMode(id, update, true)
}

func (s *sessionStore) touchStateMode(id string, update func(*sessionState), allowCreate bool) (*sessionState, error) {
	id = defaultSession(id)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errSessionClosing
	}
	now := s.now().UTC()
	expired := s.reapLocked(now)
	session := s.sessions[id]
	if session == nil {
		if !allowCreate {
			s.mu.Unlock()
			_ = s.closeSessionStates(expired)
			return nil, errSessionNotFound
		}
		if len(s.sessions) >= s.max {
			err := s.capacityErrorLocked()
			s.mu.Unlock()
			_ = s.closeSessionStates(expired)
			return nil, err
		}
		session = &sessionState{id: id, createdAt: now}
		session.lifecycle.Store(uint32(sessionActive))
		s.sessions[id] = session
	}
	session.lastUsed = now
	if update != nil {
		update(session)
	}
	s.mu.Unlock()
	_ = s.closeSessionStates(expired)
	return session, nil
}

func (s *sessionStore) create(opts sessionOptions) (*sessionState, error) {
	session, _, err := s.createOrReuse(opts, false)
	return session, err
}

func (s *sessionStore) createOrReuse(opts sessionOptions, reuse bool) (*sessionState, bool, error) {
	opts.id = defaultSession(opts.id)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, false, errSessionClosing
	}
	now := s.now().UTC()
	expired := s.reapLocked(now)
	if existing := s.sessions[opts.id]; existing != nil {
		if reuse {
			var err error
			if !existing.active() {
				err = errSessionClosing
			} else if existing.toOptions() != opts {
				err = errSessionOptions
			} else {
				existing.lastUsed = now
			}
			s.mu.Unlock()
			_ = s.closeSessionStates(expired)
			return existing, false, err
		}
		s.mu.Unlock()
		_ = s.closeSessionStates(expired)
		return nil, false, errSessionExists
	}
	if len(s.sessions) >= s.max {
		err := s.capacityErrorLocked()
		s.mu.Unlock()
		_ = s.closeSessionStates(expired)
		return nil, false, err
	}
	if opts.profilePath != "" {
		if owner := s.profilePaths[harDestinationKey(opts.profilePath)]; owner != nil {
			expiredOwner := false
			for _, retired := range expired {
				expiredOwner = expiredOwner || retired == owner
			}
			s.mu.Unlock()
			_ = s.closeSessionStates(expired)
			if expiredOwner {
				return s.createOrReuse(opts, reuse)
			}
			return nil, false, &profileOwnerError{sessionID: owner.id}
		}
	}
	if opts.har != nil && s.harPaths[harDestinationKey(opts.har.path)] != nil {
		s.mu.Unlock()
		_ = s.closeSessionStates(expired)
		return nil, false, errHARDestinationInUse
	}
	session := &sessionState{
		width:            opts.width,
		headful:          opts.headful,
		height:           opts.height,
		id:               opts.id,
		proxy:            opts.proxy,
		locale:           opts.locale,
		os:               opts.os,
		profilePath:      opts.profilePath,
		storageStatePath: opts.storageStatePath,
		har:              opts.har,
		createdAt:        now,
		lastUsed:         now,
	}
	if opts.har != nil {
		session.lifecycle.Store(uint32(sessionInitializing))
	} else {
		session.lifecycle.Store(uint32(sessionActive))
	}
	s.sessions[opts.id] = session
	if opts.profilePath != "" {
		s.profilePaths[harDestinationKey(opts.profilePath)] = session
	}
	if opts.har != nil {
		s.harPaths[harDestinationKey(opts.har.path)] = session
	}
	s.mu.Unlock()
	_ = s.closeSessionStates(expired)
	return session, true, nil
}

func (s *sessionStore) activateExpected(id string, expected *sessionState) bool {
	id = defaultSession(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.sessions[id] != expected || expected == nil {
		return false
	}
	if sessionLifecycle(expected.lifecycle.Load()) != sessionInitializing {
		return false
	}
	expected.lifecycle.Store(uint32(sessionActive))
	return true
}

func (s *sessionStore) destroy(id string, allowHAR bool) (*sessionState, error) {
	return s.destroyExpected(id, nil, allowHAR)
}

func (s *sessionStore) destroyExpected(id string, expected *sessionState, allowHAR bool) (*sessionState, error) {
	id = defaultSession(id)
	s.mu.Lock()
	session := s.sessions[id]
	if expected != nil && session != expected {
		s.mu.Unlock()
		return nil, nil
	}
	if session == nil {
		s.mu.Unlock()
		return nil, nil
	}
	if session.har != nil && !allowHAR {
		s.mu.Unlock()
		return session, errHARStopRequired
	}
	delete(s.sessions, id)
	session.lifecycle.Store(uint32(sessionClosing))
	s.mu.Unlock()
	err := closeSessionState(session)
	s.releaseSessionPaths(session)
	return session, err
}

func (s *sessionStore) destroyHAR(id string) (*sessionState, error) {
	s.mu.Lock()
	session := s.sessions[defaultSession(id)]
	if session == nil || session.har == nil {
		s.mu.Unlock()
		return nil, errHARNotRecording
	}
	s.mu.Unlock()
	return s.destroyExpected(id, session, true)
}

func (s *sessionStore) list() []map[string]any {
	s.mu.Lock()
	now := s.now().UTC()
	expired := s.reapLocked(now)
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		session := s.sessions[id]
		idle := now.Sub(session.lastUsed)
		if idle < 0 {
			idle = 0
		}
		out = append(out, map[string]any{
			"session_id":    id,
			"headful":       session.headful,
			"state_source":  session.stateSource(),
			"url":           session.url,
			"idle_ms":       idle.Milliseconds(),
			"created_at":    session.createdAt.Format(time.RFC3339Nano),
			"har_recording": session.har != nil,
		})
	}
	s.mu.Unlock()
	_ = s.closeSessionStates(expired)
	return out
}

func (s *sessionState) stateSource() string {
	if s.profilePath != "" {
		return "profile"
	}
	if s.storageStatePath != "" {
		return "storage_state"
	}
	return "ephemeral"
}

func (s *sessionStore) closeAll() error {
	s.mu.Lock()
	s.closed = true
	sessions := make([]*sessionState, 0, len(s.sessions))
	for id, session := range s.sessions {
		delete(s.sessions, id)
		session.lifecycle.Store(uint32(sessionClosing))
		sessions = append(sessions, session)
	}
	s.mu.Unlock()
	return s.closeSessionStates(sessions)
}

func (s *sessionStore) reapLocked(now time.Time) []*sessionState {
	if s.ttl <= 0 {
		return nil
	}
	var expired []*sessionState
	for id, session := range s.sessions {
		if now.Sub(session.lastUsed) > s.ttl {
			delete(s.sessions, id)
			session.lifecycle.Store(uint32(sessionClosing))
			expired = append(expired, session)
		}
	}
	return expired
}

func (s *sessionState) active() bool {
	return s != nil && sessionLifecycle(s.lifecycle.Load()) == sessionActive
}

func (s *sessionStore) closeSessionStates(sessions []*sessionState) error {
	var result error
	for _, session := range sessions {
		result = errors.Join(result, closeSessionState(session))
		s.releaseSessionPaths(session)
	}
	return result
}

func (s *sessionStore) releaseSessionPaths(session *sessionState) {
	if session == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if session.har != nil {
		key := harDestinationKey(session.har.path)
		if s.harPaths[key] == session {
			delete(s.harPaths, key)
		}
	}
	if session.profilePath != "" {
		key := harDestinationKey(session.profilePath)
		if s.profilePaths[key] == session {
			delete(s.profilePaths, key)
		}
	}
}

func harDestinationKey(path string) string {
	key := filepath.Clean(path)
	if sessionStoreGOOS == "darwin" || sessionStoreGOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}

func closeSessionState(session *sessionState) error {
	if session == nil {
		return nil
	}
	session.opMu.Lock()
	defer session.opMu.Unlock()
	if sessionLifecycle(session.lifecycle.Load()) == sessionClosed {
		return nil
	}
	if session.harTimer != nil {
		session.harTimer.Stop()
		session.harTimer = nil
	}
	var err error
	if session.browser != nil {
		err = session.browser.Close()
		if provider, ok := session.browser.(interface {
			HARResult() (gomoufox.HARResult, bool)
		}); ok {
			if result, available := provider.HARResult(); available {
				session.harResult = &result
			}
		}
		session.browser = nil
	}
	session.lifecycle.Store(uint32(sessionClosed))
	return err
}

func sessionError(err error) Response {
	var owner *profileOwnerError
	if errors.As(err, &owner) {
		return Response{IsError: true, Payload: map[string]any{
			"error": "profile_in_use", "owner_session_id": owner.sessionID,
			"hint": "Reuse owner_session_id, or wait for session_destroy to finish before acquiring its profile. Do not remove browser lock files.",
		}}
	}
	switch {
	case errors.Is(err, errSessionLimit):
		response := mcpError("session_limit")
		response.Payload["next_tool"] = "session_list"
		response.Payload["hint"] = "Creation failed; do not navigate with the rejected name. Reuse an active session, or inspect session_list and destroy a session you no longer need before retrying. Stop its HAR recording first if present."
		var capacity *sessionCapacityError
		if errors.As(err, &capacity) {
			response.Payload["capacity"] = map[string]int{"used": capacity.used, "limit": capacity.limit, "available": capacity.limit - capacity.used}
			response.Payload["active_session_ids"] = capacity.ids
			response.Payload["session_ids_omitted"] = capacity.omitted
		}
		return response
	case errors.Is(err, errSessionExists):
		response := mcpError("session_exists")
		response.Payload["hint"] = "Inspect session_list, then acquire the intended session with reuse_if_exists:true and the same configuration, or choose a different name."
		return response
	case errors.Is(err, errSessionOptions):
		response := mcpError("session_options_mismatch")
		response.Payload["hint"] = "The existing session has different settings. Reuse it with the same creation configuration or choose a different name; no settings were changed."
		return response
	case errors.Is(err, errSessionNotFound):
		response := mcpError("session_not_found")
		response.Payload["next_tool"] = "session_create"
		response.Payload["hint"] = "Required session does not exist or expired. Successfully acquire it with session_create before navigation; no browser was started."
		return response
	case errors.Is(err, errSessionClosing):
		return mcpError("session_closed")
	case errors.Is(err, errHARDestinationInUse):
		return mcpError("har_destination_exists")
	default:
		return browserFailure("session_error", err, "session")
	}
}
