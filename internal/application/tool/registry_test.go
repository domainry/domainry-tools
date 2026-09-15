package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sdk "github.com/domainry/domainry-tools-sdk"
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

func TestRegistrationAcceptsOnlyExplicitReadParallelism(t *testing.T) {
	base := sdk.Definition{Key: "read", Version: "1", Description: "Read", ActionKey: "records.read", Effect: "read", Idempotency: "natural", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), TimeoutMillis: 1000, MaxOutputBytes: 1024}
	handler := func(context.Context, sdk.Request) (sdk.Result, error) {
		return sdk.Result{Status: "completed", Content: json.RawMessage(`{}`)}, nil
	}
	authorize := func(context.Context, sdk.Request) (sdk.Authorization, error) {
		return sdk.Authorization{Granted: true}, nil
	}
	valid := base
	valid.Parallelism = sdk.ToolParallelismIndependentRead
	if err := NewRegistry().Register(Registration{Definition: valid, Authorize: authorize, Invoke: handler}); err != nil {
		t.Fatal(err)
	}
	unknown := base
	unknown.Parallelism = "parallel"
	if err := NewRegistry().Register(Registration{Definition: unknown, Authorize: authorize, Invoke: handler}); err == nil {
		t.Fatal("unknown parallelism accepted")
	}
	write := base
	write.Key, write.ActionKey, write.Effect, write.Idempotency, write.Parallelism = "write", "records.write", "write", "key", sdk.ToolParallelismIndependentRead
	if err := NewRegistry().Register(Registration{Definition: write, Authorize: authorize, Invoke: handler, Reconcile: handler}); err == nil {
		t.Fatal("write declared as independent read")
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

func TestOutcomeInspectionCannotInvokeOrRetry(t *testing.T) {
	r := NewRegistry()
	reads, writes := 0, 0
	allowed := true
	d := sdk.Definition{Key: "write", Version: "1", Description: "Write", ActionKey: "records.write", Effect: "write", Idempotency: "key", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), TimeoutMillis: 1000, MaxOutputBytes: 1024}
	writer := func(context.Context, sdk.Request) (sdk.Result, error) {
		writes++
		return sdk.Result{Status: "completed", Content: json.RawMessage(`{}`)}, nil
	}
	if err := r.Register(Registration{Definition: d, Authorize: func(context.Context, sdk.Request) (sdk.Authorization, error) {
		return sdk.Authorization{Granted: allowed}, nil
	}, Invoke: writer, Reconcile: writer, InspectOutcome: func(_ context.Context, in sdk.Request) (sdk.Result, error) {
		reads++
		if in.IdempotencyKey != "original" {
			t.Fatal("changed key")
		}
		return sdk.Result{Status: "completed", Content: json.RawMessage(`{"id":"existing"}`)}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	host, err := r.Select([]string{"write"})
	if err != nil {
		t.Fatal(err)
	}
	in := sdk.Request{Definition: d, Call: sdk.Call{Name: "write", Arguments: `{}`}, IdempotencyKey: "original", OutcomeInspectionToken: "inspection"}
	if _, err = host.InvokeConversationTool(t.Context(), in); err == nil {
		t.Fatal("inspection invoked")
	}
	if _, err = host.ReconcileConversationTool(t.Context(), in); err == nil {
		t.Fatal("inspection retried")
	}
	if _, err = host.InspectConversationToolOutcome(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	allowed = false
	if _, err = host.InspectConversationToolOutcome(t.Context(), in); err == nil {
		t.Fatal("revocation ignored")
	}
	if writes != 0 || reads != 1 {
		t.Fatalf("reads %d writes %d", reads, writes)
	}
}
