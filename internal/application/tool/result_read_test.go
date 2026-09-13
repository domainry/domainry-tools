package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdk "github.com/domainry/domainry-tools-sdk"
)

func TestIndependentResultReadRequiresExplicitCurrentSourcePolicy(t *testing.T) {
	definition := sdk.Definition{Key: "specialist", Version: "1", ActionKey: "specialist.execute", Effect: "write", Idempotency: "key", TimeoutMillis: 1000, MaxOutputBytes: 1024, InputSchema: json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"string"}},"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"string"}},"additionalProperties":false}`)}
	invocations, executionChecks, sourceChecks := 0, 0, 0
	sourceAllowed := true
	registration := Registration{Definition: definition, Authorize: func(context.Context, sdk.Request) (sdk.Authorization, error) {
		executionChecks++
		return sdk.Authorization{}, nil
	}, Invoke: func(context.Context, sdk.Request) (sdk.Result, error) {
		invocations++
		return sdk.Result{}, nil
	}, AuthorizeResultRead: func(_ context.Context, in sdk.Request, result sdk.Result) error {
		sourceChecks++
		if !sourceAllowed || in.Authority.UserID != "reader" || in.Call.Arguments != `{"id":"saved"}` || string(result.Content) != `{"id":"saved"}` {
			return denied()
		}
		return nil
	}}
	registration.Reconcile = registration.Invoke
	registry := NewRegistry()
	if err := registry.Register(registration); err != nil {
		t.Fatal(err)
	}
	selected, err := registry.Select([]string{definition.Key})
	if err != nil {
		t.Fatal(err)
	}
	request := sdk.Request{Authority: sdk.Authority{Known: true, RuntimeID: "runtime", WorkspaceID: "workspace", UserID: "reader"}, Definition: definition, Call: sdk.Call{Name: definition.Key, Arguments: `{"id":"saved"}`}}
	result := sdk.Result{Status: "completed", Content: json.RawMessage(`{"id":"saved"}`)}
	if err := selected.AuthorizeConversationToolResultRead(t.Context(), request, result); err != nil || executionChecks != 0 || sourceChecks != 1 {
		t.Fatalf("independent read: %v, execution=%d source=%d", err, executionChecks, sourceChecks)
	}
	if _, err := selected.InvokeConversationTool(t.Context(), request); err == nil {
		t.Fatal("result read granted execution")
	}
	if _, err := selected.ReconcileConversationTool(t.Context(), request); err == nil {
		t.Fatal("result read granted reconciliation")
	}
	if err := selected.AuthorizeConversationToolResult(t.Context(), request, result); err == nil {
		t.Fatal("independent read authorized raw replay")
	}
	sourceAllowed = false
	if err := selected.AuthorizeConversationToolResultRead(t.Context(), request, result); err == nil {
		t.Fatal("source revocation ignored")
	}
	sourceAllowed = true
	changed := request
	changed.Definition.Version = "2"
	if err := selected.AuthorizeConversationToolResultRead(t.Context(), changed, result); err == nil {
		t.Fatal("changed definition accepted")
	}
	changed = request
	changed.Call.Arguments = `{"id":"other"}`
	if err := selected.AuthorizeConversationToolResultRead(t.Context(), changed, result); err == nil {
		t.Fatal("changed source request accepted")
	}
	if invocations != 0 {
		t.Fatal("read or denied execution reached effect")
	}
	registration.AuthorizeResultRead = nil
	// A legacy authorizer that accepts a result must not imply the new policy.
	registration.AuthorizeResult = func(context.Context, sdk.Request, sdk.Result) error { return nil }
	legacyRegistry := NewRegistry()
	if err := legacyRegistry.Register(registration); err != nil {
		t.Fatal(err)
	}
	legacy, _ := legacyRegistry.Select([]string{definition.Key})
	combined, _ := Combine(selected, legacy, []string{definition.Key})
	var coded *sdk.Error
	if err := combined.AuthorizeConversationToolResultRead(t.Context(), request, result); !errors.As(err, &coded) || coded.Code != sdk.ResultReadUnsupportedCode {
		t.Fatalf("combined host used another registration or legacy source policy: %v", err)
	}
	combined, _ = Combine(legacy, selected, []string{definition.Key})
	if err := combined.AuthorizeConversationToolResultRead(t.Context(), request, result); err != nil {
		t.Fatal(err)
	}
}
