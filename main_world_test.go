package gomoufox

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type mainWorldPage struct {
	fakePage
	evaluate func(string, ...any) (any, error)
}

func (p *mainWorldPage) Evaluate(s string, a ...any) (any, error) { return p.evaluate(s, a...) }

func TestMainWorldEvaluationBindsJSONAndAwaitsCompletion(t *testing.T) {
	for _, async := range []bool{false, true} {
		calls := []string{}
		raw := &mainWorldPage{evaluate: func(s string, a ...any) (any, error) {
			calls = append(calls, s)
			if len(a) != 0 {
				t.Fatal("unbound protocol argument")
			}
			if strings.HasPrefix(s, "mw:delete globalThis") {
				return true, nil
			}
			if len(calls) == 1 {
				if !strings.Contains(s, `JSON.parse("{\"__proto__\":{\"safe\":true},\"text\":\"quote\\\"\\n\"}")`) || !strings.Contains(s, "arg => arg.text // trailing comment\n)") {
					t.Fatalf("binding=%s", s)
				}
				if async {
					return map[string]any{"status": "pending"}, nil
				}
			}
			return map[string]any{"status": "done", "value": "result"}, nil
		}}
		p := &Page{raw: raw}
		result, err := p.Evaluate(context.Background(), " mw:arg => arg.text // trailing comment", map[string]any{"__proto__": map[string]any{"safe": true}, "text": "quote\"\n"})
		if err != nil || result != "result" {
			t.Fatalf("result=%v err=%v", result, err)
		}
		if async && (len(calls) != 3 || !strings.Contains(calls[2], "delete globalThis")) {
			t.Fatalf("pending cleanup=%v", calls)
		}
	}
}

func TestMainWorldEvaluationFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result any
		err    error
	}{
		{"invalid transport", 1, nil}, {"missing status", map[string]any{}, nil},
		{"rejection", map[string]any{"status": "error", "message": "fixture rejection"}, nil},
		{"driver failure", nil, errors.New("transport failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Page{raw: &mainWorldPage{evaluate: func(string, ...any) (any, error) { return tc.result, tc.err }}}
			if _, err := p.Evaluate(context.Background(), "mw:1"); err == nil {
				t.Fatal("failure accepted")
			}
		})
	}
	p := &Page{raw: &mainWorldPage{}}
	if _, err := p.Evaluate(context.Background(), "mw:1", 1, 2); err == nil {
		t.Fatal("multiple args accepted")
	}
	if _, err := p.Evaluate(context.Background(), "mw:1", make(chan int)); err == nil {
		t.Fatal("non-JSON argument accepted")
	}
}

func TestMainWorldPollingFailureCleansPendingResult(t *testing.T) {
	failure := errors.New("polling connection failed")
	calls, cleaned := 0, false
	raw := &mainWorldPage{evaluate: func(s string, _ ...any) (any, error) {
		if strings.HasPrefix(s, "mw:delete globalThis") {
			cleaned = true
			return nil, nil
		}
		calls++
		if calls == 1 {
			return map[string]any{"status": "pending"}, nil
		}
		return nil, failure
	}}
	_, err := (&Page{raw: raw}).Evaluate(context.Background(), "mw:Promise.resolve(1)")
	if !errors.Is(err, failure) || !cleaned || calls != 2 {
		t.Fatalf("poll = %v, cleaned=%t, calls=%d", err, cleaned, calls)
	}
}

func TestMainWorldPendingEvaluationHonorsDeadline(t *testing.T) {
	cleaned := make(chan struct{})
	raw := &mainWorldPage{evaluate: func(s string, _ ...any) (any, error) {
		if strings.HasPrefix(s, "mw:delete globalThis") {
			close(cleaned)
			return true, nil
		}
		return map[string]any{"status": "pending"}, nil
	}}
	p := &Page{raw: raw}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := p.Evaluate(ctx, "mw:new Promise(()=>{})")
	if !errors.Is(err, ErrEvaluationInterrupted) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v", err)
	}
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("pending job not cleaned")
	}
	if raw.closeCalls != 1 {
		t.Fatalf("page close=%d", raw.closeCalls)
	}
}

func TestLiveMainWorldCancellation(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE") != "1" {
		t.Skip("set GOMOUFOX_LIVE=1 for main-world cancellation")
	}
	b, err := New(context.Background(), WithAutoInstall(false), WithMainWorldEval(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	page, err := b.NewPage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = page.Evaluate(ctx, `mw:async () => {await new Promise(resolve=>setTimeout(resolve,300));return 7;}`)
	if !errors.Is(err, ErrEvaluationInterrupted) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pending script=%v", err)
	}
	if _, err := page.EvaluateInternal(context.Background(), "1+1"); err == nil {
		t.Fatal("page survived interruption")
	}
}
