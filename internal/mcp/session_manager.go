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
	errSessionClosing      = errors.New("session is closing")
	errHARStopRequired     = errors.New("HAR stop required")
	errHARNotRecording     = errors.New("session is not recording HAR")
	errHARDestinationInUse = errors.New("HAR destination is already in use")
)

type sessionLifecycle uint32

const (
	sessionInitializing sessionLifecycle = iota
	sessionActive
	sessionClosing
	sessionClosed
)

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionState
	harPaths map[string]*sessionState
	max      int
	ttl      time.Duration
	now      func() time.Time
	closed   bool
}

type sessionState struct {
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
		sessions: map[string]*sessionState{},
		harPaths: map[string]*sessionState{},
		max:      max,
		ttl:      ttl,
		now:      time.Now,
	}
}

func (s *sessionStore) touch(id string, update func(*sessionState)) error {
	_, err := s.touchState(id, update)
	return err
}

func (s *sessionStore) touchState(id string, update func(*sessionState)) (*sessionState, error) {
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
		if len(s.sessions) >= s.max {
			s.mu.Unlock()
			_ = s.closeSessionStates(expired)
			return nil, errSessionLimit
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
	opts.id = defaultSession(opts.id)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errSessionClosing
	}
	now := s.now().UTC()
	expired := s.reapLocked(now)
	if s.sessions[opts.id] != nil {
		s.mu.Unlock()
		_ = s.closeSessionStates(expired)
		return nil, errSessionExists
	}
	if len(s.sessions) >= s.max {
		s.mu.Unlock()
		_ = s.closeSessionStates(expired)
		return nil, errSessionLimit
	}
	if opts.har != nil && s.harPaths[harDestinationKey(opts.har.path)] != nil {
		s.mu.Unlock()
		_ = s.closeSessionStates(expired)
		return nil, errHARDestinationInUse
	}
	session := &sessionState{
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
	if opts.har != nil {
		s.harPaths[harDestinationKey(opts.har.path)] = session
	}
	s.mu.Unlock()
	_ = s.closeSessionStates(expired)
	return session, nil
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
	s.releaseHARPath(session)
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
		s.releaseHARPath(session)
	}
	return result
}

func (s *sessionStore) releaseHARPath(session *sessionState) {
	if session == nil || session.har == nil {
		return
	}
	key := harDestinationKey(session.har.path)
	s.mu.Lock()
	if s.harPaths[key] == session {
		delete(s.harPaths, key)
	}
	s.mu.Unlock()
}

func harDestinationKey(path string) string {
	key := filepath.Clean(path)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
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
	switch {
	case errors.Is(err, errSessionLimit):
		return mcpError("session_limit")
	case errors.Is(err, errSessionExists):
		return mcpError("session_exists")
	case errors.Is(err, errSessionClosing):
		return mcpError("session_closed")
	case errors.Is(err, errHARDestinationInUse):
		return mcpError("har_destination_exists")
	default:
		return mcpError("session_error")
	}
}
