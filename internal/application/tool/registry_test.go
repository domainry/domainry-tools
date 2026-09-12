package tools

import (
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/domainry/domainry-tools-sdk"
	"testing"
	"time"
)

func TestSelectionsValidateAndReauthorize(t *testing.T) {
	r := NewRegistry()
	allowed := true
	calls := 0
	d := sdk.Definition{Key: "read", Version: "1", Description: "Read", ActionKey: "records.read", Effect: "read", Idempotency: "natural", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object"}`), TimeoutMillis: 1000, MaxOutputBytes: 1024}
	err := r.Register(Registration{Definition: d, Authorize: func(context.Context, sdk.Request) (sdk.Authorization, error) {
		return sdk.Authorization{Granted: allowed}, nil
	}, Invoke: func(context.Context, sdk.Request) (sdk.Result, error) {
		calls++
		return sdk.Result{Status: "completed", Content: json.RawMessage(`{}`)}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := r.Select([]string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	none, _ := r.Select(nil)
	in := sdk.Request{Definition: d, Call: sdk.Call{Name: "read", Arguments: `{"id":"one"}`}}
	if _, e := none.InvokeConversationTool(t.Context(), in); e == nil {
		t.Fatal("unselected tool executed")
	}
	in.Call.Arguments = `{"id":"one","owner":"other"}`
	if _, e := selected.InvokeConversationTool(t.Context(), in); e == nil {
		t.Fatal("extra scope argument accepted")
	}
	if calls != 0 {
		t.Fatal("invalid input reached handler")
	}
	in.Call.Arguments = `{"id":"one"}`
	if _, e := selected.InvokeConversationTool(t.Context(), in); e != nil {
		t.Fatal(e)
	}
	allowed = false
	if _, e := selected.InvokeConversationTool(t.Context(), in); e == nil {
		t.Fatal("revoked permission ignored")
	}
	if calls != 1 {
		t.Fatal("revoked tool executed")
	}
	d.Version = "2"
	in.Definition = d
	allowed = true
	if _, e := selected.InvokeConversationTool(t.Context(), in); e == nil {
		t.Fatal("changed definition accepted")
	}
}

func TestSelectionAppliesOwnerDefinitionTimeout(t *testing.T) {
	registry := NewRegistry()
	definition := sdk.Definition{Key: "slow", Version: "1", Description: "Slow read", ActionKey: "slow.read", Effect: "read", Idempotency: "natural", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object"}`), TimeoutMillis: 25, MaxOutputBytes: 1024}
	started := make(chan struct{})
	if err := registry.Register(Registration{Definition: definition, Authorize: func(context.Context, sdk.Request) (sdk.Authorization, error) {
		return sdk.Authorization{Granted: true}, nil
	}, Invoke: func(ctx context.Context, _ sdk.Request) (sdk.Result, error) {
		close(started)
		<-ctx.Done()
		return sdk.Result{}, ctx.Err()
	}}); err != nil {
		t.Fatal(err)
	}
	selected, err := registry.Select([]string{"slow"})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	_, err = selected.InvokeConversationTool(t.Context(), sdk.Request{Definition: definition, Call: sdk.Call{Name: "slow", Arguments: `{}`}})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(before) > time.Second {
		t.Fatalf("elapsed=%s err=%v", time.Since(before), err)
	}
	select {
	case <-started:
	default:
		t.Fatal("tool handler was not invoked")
	}
}
