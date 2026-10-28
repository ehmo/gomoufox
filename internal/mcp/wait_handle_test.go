package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ehmo/gomoufox"
)

func TestMCPWaitReleasesSelectorHandles(t *testing.T) {
	for _, kind := range []string{"selector", "text"} {
		t.Run(kind, func(t *testing.T) {
			raw := &trackedWaitElement{}
			handle := &gomoufox.ElementHandle{}
			setField(handle, "raw", raw)
			session := &gomoufoxSession{page: pageAdapter{
				waitSelector: func(context.Context, string, ...gomoufox.WaitForSelectorOption) (*gomoufox.ElementHandle, error) {
					return handle, nil
				},
			}}
			for range 100 {
				if err := session.WaitFor(context.Background(), waitCondition{Kind: kind, Value: "ready", Timeout: time.Second}); err != nil {
					t.Fatal(err)
				}
			}
			if raw.disposed != 100 {
				t.Fatalf("disposed %d of 100 wait handles", raw.disposed)
			}
			boom := errors.New("dispose failed")
			raw.err = boom
			if err := session.WaitFor(context.Background(), waitCondition{Kind: kind, Value: "ready"}); !errors.Is(err, boom) {
				t.Fatalf("cleanup error = %v", err)
			}
		})
	}
}

type trackedWaitElement struct {
	disposed int
	err      error
}

func (e *trackedWaitElement) Raw() any { return nil }
func (e *trackedWaitElement) Dispose() error {
	e.disposed++
	return e.err
}
