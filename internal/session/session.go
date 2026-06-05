package session

import (
	"errors"
	"sort"
	"sync"
	"time"
)

var (
	ErrMaxSessions   = errors.New("max sessions reached")
	ErrInvalidConfig = errors.New("invalid session config")
)

const (
	DefaultID          = "default"
	DefaultMaxSessions = 5
	HardMaxSessions    = 20
	DefaultTTL         = 30 * time.Minute
	MaxTTL             = 24 * time.Hour
)

type Closer interface {
	Close() error
}

type Entry struct {
	ID         string
	URL        string
	CreatedAt  time.Time
	LastUsedAt time.Time
	Persistent bool
	Resource   Closer
}

type Manager struct {
	mu       sync.Mutex
	max      int
	ttl      time.Duration
	now      func() time.Time
	sessions map[string]*Entry
}

func New(max int, ttl time.Duration, now func() time.Time) (*Manager, error) {
	if max <= 0 || max > HardMaxSessions || ttl <= 0 || ttl > MaxTTL {
		return nil, ErrInvalidConfig
	}
	if now == nil {
		now = time.Now
	}
	return &Manager{max: max, ttl: ttl, now: now, sessions: map[string]*Entry{}}, nil
}

func (m *Manager) GetOrCreate(id string, create func() (*Entry, error)) (*Entry, error) {
	if id == "" {
		id = DefaultID
	}
	var expired []*Entry
	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		closeEntries(expired)
	}()
	expired = m.reapLocked()
	if entry, ok := m.sessions[id]; ok {
		entry.LastUsedAt = m.now()
		return entry, nil
	}
	if len(m.sessions) >= m.max {
		return nil, ErrMaxSessions
	}
	entry, err := create()
	if err != nil {
		return nil, err
	}
	if entry == nil {
		entry = &Entry{}
	}
	now := m.now()
	entry.ID = id
	entry.CreatedAt = now
	entry.LastUsedAt = now
	m.sessions[id] = entry
	return entry, nil
}

func (m *Manager) Destroy(id string) bool {
	if id == "" {
		id = DefaultID
	}
	m.mu.Lock()
	entry, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if ok && entry.Resource != nil {
		_ = entry.Resource.Close()
	}
	return ok
}

func (m *Manager) List() []Entry {
	var expired []*Entry
	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		closeEntries(expired)
	}()
	expired = m.reapLocked()
	out := make([]Entry, 0, len(m.sessions))
	for _, entry := range m.sessions {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *Manager) Touch(id, currentURL string) {
	if id == "" {
		id = DefaultID
	}
	var expired []*Entry
	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		closeEntries(expired)
	}()
	expired = m.reapLocked()
	if entry, ok := m.sessions[id]; ok {
		entry.LastUsedAt = m.now()
		entry.URL = currentURL
	}
}

func (m *Manager) Reap() int {
	var expired []*Entry
	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		closeEntries(expired)
	}()
	expired = m.reapLocked()
	return len(expired)
}

func (m *Manager) reapLocked() []*Entry {
	now := m.now()
	reaped := make([]*Entry, 0)
	for id, entry := range m.sessions {
		if now.Sub(entry.LastUsedAt) > m.ttl {
			delete(m.sessions, id)
			reaped = append(reaped, entry)
		}
	}
	return reaped
}

func closeEntries(entries []*Entry) {
	for _, entry := range entries {
		if entry.Resource != nil {
			_ = entry.Resource.Close()
		}
	}
}
