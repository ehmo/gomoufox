package mcp

import (
	"errors"
	"sort"
	"sync"
	"time"
)

var (
	errSessionLimit  = errors.New("session limit reached")
	errSessionExists = errors.New("session already exists")
)

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionState
	max      int
	ttl      time.Duration
	now      func() time.Time
}

type sessionState struct {
	id               string
	url              string
	proxy            string
	locale           string
	os               string
	profilePath      string
	storageStatePath string
	createdAt        time.Time
	lastUsed         time.Time
	opMu             sync.Mutex
	browser          browserSession
}

type sessionOptions struct {
	id               string
	proxy            string
	locale           string
	os               string
	profilePath      string
	storageStatePath string
}

func newSessionStore(max int, ttl time.Duration) *sessionStore {
	return &sessionStore{
		sessions: map[string]*sessionState{},
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
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.reapLocked(now)
	session := s.sessions[id]
	if session == nil {
		if len(s.sessions) >= s.max {
			return nil, errSessionLimit
		}
		session = &sessionState{id: id, createdAt: now}
		s.sessions[id] = session
	}
	session.lastUsed = now
	if update != nil {
		update(session)
	}
	return session, nil
}

func (s *sessionStore) create(opts sessionOptions) error {
	opts.id = defaultSession(opts.id)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.reapLocked(now)
	if s.sessions[opts.id] != nil {
		return errSessionExists
	}
	if len(s.sessions) >= s.max {
		return errSessionLimit
	}
	s.sessions[opts.id] = &sessionState{
		id:               opts.id,
		proxy:            opts.proxy,
		locale:           opts.locale,
		os:               opts.os,
		profilePath:      opts.profilePath,
		storageStatePath: opts.storageStatePath,
		createdAt:        now,
		lastUsed:         now,
	}
	return nil
}

func (s *sessionStore) destroy(id string) {
	s.mu.Lock()
	session := s.sessions[defaultSession(id)]
	delete(s.sessions, defaultSession(id))
	s.mu.Unlock()
	closeSessionBrowser(session)
}

func (s *sessionStore) list() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.reapLocked(now)
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
			"session_id": id,
			"url":        session.url,
			"idle_ms":    idle.Milliseconds(),
			"created_at": session.createdAt.Format(time.RFC3339Nano),
		})
	}
	return out
}

func (s *sessionStore) reapLocked(now time.Time) {
	if s.ttl <= 0 {
		return
	}
	for id, session := range s.sessions {
		if now.Sub(session.lastUsed) > s.ttl {
			delete(s.sessions, id)
			closeSessionBrowser(session)
		}
	}
}

func closeSessionBrowser(session *sessionState) {
	if session != nil && session.browser != nil {
		_ = session.browser.Close()
		session.browser = nil
	}
}

func sessionError(err error) Response {
	switch {
	case errors.Is(err, errSessionLimit):
		return mcpError("session_limit")
	case errors.Is(err, errSessionExists):
		return mcpError("session_exists")
	default:
		return mcpError("session_error")
	}
}
