package session

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerCreateListTouchDestroy(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	m, err := New(2, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	entry, err := m.GetOrCreate("", func() (*Entry, error) { return &Entry{URL: "about:blank"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "default" || entry.CreatedAt != now {
		t.Fatalf("entry = %#v", entry)
	}
	now = now.Add(time.Minute)
	m.Touch("default", "https://example.com")
	list := m.List()
	if len(list) != 1 || list[0].URL != "https://example.com" || list[0].LastUsedAt != now {
		t.Fatalf("list = %#v", list)
	}
	if !m.Destroy("default") || m.Destroy("default") {
		t.Fatalf("destroy result wrong")
	}
}

func TestManagerMaxTTLAndClose(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	m, err := New(1, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	closer := &fakeCloser{}
	if _, err := m.GetOrCreate("a", func() (*Entry, error) { return &Entry{Resource: closer}, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetOrCreate("b", func() (*Entry, error) { return nil, nil }); !errors.Is(err, ErrMaxSessions) {
		t.Fatalf("max err = %v", err)
	}
	now = now.Add(2 * time.Minute)
	if reaped := m.Reap(); reaped != 1 {
		t.Fatalf("reaped = %d", reaped)
	}
	for i := 0; i < 100 && !closer.closed.Load(); i++ {
		time.Sleep(time.Millisecond)
	}
	if !closer.closed.Load() {
		t.Fatalf("resource not closed")
	}
}

func TestManagerReapsExpiredSessionBeforeReusingID(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	m, err := New(1, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	oldCloser := &fakeCloser{}
	first, err := m.GetOrCreate("acct", func() (*Entry, error) {
		return &Entry{URL: "https://old.example", Resource: oldCloser}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute + time.Nanosecond)
	newCloser := &fakeCloser{}
	second, err := m.GetOrCreate("acct", func() (*Entry, error) {
		return &Entry{URL: "https://new.example", Resource: newCloser}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("expired session was reused")
	}
	if !oldCloser.closed.Load() {
		t.Fatalf("expired resource was not closed before GetOrCreate returned")
	}
	list := m.List()
	if len(list) != 1 || list[0].URL != "https://new.example" {
		t.Fatalf("sessions after reuse = %#v", list)
	}
}

func TestManagerTouchDoesNotResurrectExpiredSession(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	m, err := New(1, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	closer := &fakeCloser{}
	if _, err := m.GetOrCreate("acct", func() (*Entry, error) {
		return &Entry{URL: "https://old.example", Resource: closer}, nil
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute + time.Nanosecond)
	m.Touch("acct", "https://new.example")
	if !closer.closed.Load() {
		t.Fatalf("expired resource was not closed by Touch")
	}
	if list := m.List(); len(list) != 0 {
		t.Fatalf("expired session survived Touch: %#v", list)
	}
}

func TestManagerDefaultNowReuseErrorsNilEntriesAndSorting(t *testing.T) {
	defaultClock, err := New(1, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	defaultEntry, err := defaultClock.GetOrCreate("clock", func() (*Entry, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if defaultEntry.CreatedAt.IsZero() || defaultEntry.LastUsedAt.IsZero() {
		t.Fatalf("default clock entry = %#v", defaultEntry)
	}

	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	m, err := New(3, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if entry, err := m.GetOrCreate("bad", func() (*Entry, error) { return nil, boom }); !errors.Is(err, boom) || entry != nil {
		t.Fatalf("create error entry/err = %#v/%v", entry, err)
	}
	entry, err := m.GetOrCreate("mid", func() (*Entry, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "mid" || entry.CreatedAt != now {
		t.Fatalf("nil-created entry = %#v", entry)
	}
	now = now.Add(time.Minute)
	reused, err := m.GetOrCreate("mid", func() (*Entry, error) {
		t.Fatalf("create called for existing session")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if reused != entry || reused.LastUsedAt != now {
		t.Fatalf("reused entry = %#v", reused)
	}
	if _, err := m.GetOrCreate("z", func() (*Entry, error) { return &Entry{}, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetOrCreate("a", func() (*Entry, error) { return &Entry{}, nil }); err != nil {
		t.Fatal(err)
	}
	list := m.List()
	if len(list) != 3 || list[0].ID != "a" || list[1].ID != "mid" || list[2].ID != "z" {
		t.Fatalf("sorted list = %#v", list)
	}

	closer := &fakeCloser{}
	defaults, err := New(1, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := defaults.GetOrCreate("", func() (*Entry, error) { return &Entry{Resource: closer}, nil }); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	defaults.Touch("", "https://default.example")
	if list := defaults.List(); len(list) != 1 || list[0].URL != "https://default.example" || list[0].LastUsedAt != now {
		t.Fatalf("default touch list = %#v", list)
	}
	if !defaults.Destroy("") || !closer.closed.Load() {
		t.Fatalf("default destroy did not close resource")
	}
}

func TestManagerRejectsInvalidConfig(t *testing.T) {
	for _, tc := range []struct {
		max int
		ttl time.Duration
	}{
		{0, time.Minute},
		{21, time.Minute},
		{1, 0},
		{1, 25 * time.Hour},
	} {
		if _, err := New(tc.max, tc.ttl, nil); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("New(%d,%s) err = %v", tc.max, tc.ttl, err)
		}
	}
}

type fakeCloser struct{ closed atomic.Bool }

func (f *fakeCloser) Close() error {
	f.closed.Store(true)
	return nil
}
