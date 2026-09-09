package gomoufox

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// evaluateMainWorld adapts Camoufox's JSON-only main-world transport: it does not
// bind protocol arguments or await promises. Pending results live under a unique
// temporary page property and are removed after completion or cancellation.
func (p *Page) evaluateMainWorld(ctx context.Context, expression string, args ...any) (any, error) {
	if len(args) > 1 {
		return nil, errors.New("main-world evaluation accepts one argument")
	}
	var arg any
	if len(args) == 1 {
		arg = args[0]
	}
	encoded, err := json.Marshal(arg)
	if err != nil {
		return nil, err
	}
	literal, _ := json.Marshal(string(encoded))
	key := "__gomo_eval_" + rand.Text()
	keyLiteral, _ := json.Marshal(key)
	script := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(expression), "mw:"))
	initial := `mw:((expression, argument, key) => {
 const value = typeof expression === 'function' ? expression(argument) : expression;
 if (!value || typeof value.then !== 'function') return {status:'done', value};
 const job = {status:'pending'};
 Object.defineProperty(globalThis, key, {value:job, configurable:true});
 try {
   Promise.resolve(value).then(value => {job.value=value; job.status='done';}, error => {job.message=String(error);job.status='error';});
 } catch (error) { delete globalThis[key]; throw error; }
 return {status:'pending'};
})((` + script + "\n" + `), JSON.parse(` + string(literal) + `), ` + string(keyLiteral) + `)`

	poll := `mw:globalThis[` + string(keyLiteral) + `]`
	cleanup := `mw:delete globalThis[` + string(keyLiteral) + `]`
	evaluate := func(_ string, _ ...any) (any, error) {
		result, err := p.raw.Evaluate(initial)
		if err != nil {
			return nil, err
		}
		pending := false
		defer func() {
			if pending {
				_, _ = p.raw.Evaluate(cleanup)
			}
		}()
		for {
			state, ok := result.(map[string]any)
			if !ok {
				return nil, errors.New("main-world evaluation returned an invalid result")
			}
			switch state["status"] {
			case "done":
				return state["value"], nil
			case "error":
				return nil, fmt.Errorf("main-world JavaScript rejected: %v", state["message"])
			case "pending":
				pending = true
			default:
				return nil, errors.New("main-world evaluation result is unavailable")
			}
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			result, err = p.raw.Evaluate(poll)
			if err != nil {
				return nil, err
			}
		}
	}
	return p.evaluateWithContext(ctx, evaluate, "")
}
