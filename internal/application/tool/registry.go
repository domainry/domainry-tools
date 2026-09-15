// Package tools owns registered tool implementations. Engines depend on the
// SDK execution port; a product chooses which registrations to expose.
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools-sdk/schema"
)

type Handler func(context.Context, sdk.Request) (sdk.Result, error)
type Authorizer func(context.Context, sdk.Request) (sdk.Authorization, error)
type ResultAuthorizer func(context.Context, sdk.Request, sdk.Result) error

// Registration is installed by trusted product composition, never model input.
// Business handlers own their effects and durable idempotency receipts.
type Registration struct {
	InspectOutcome  Handler // optional, strictly receipt-only; never alias a mutating retry
	Definition      sdk.Definition
	Authorize       Authorizer
	Invoke          Handler
	Reconcile       Handler
	AuthorizeResult ResultAuthorizer
	// Explicit source-owned read policy, independent of Authorize. Nil denies
	// result reading without execution authority; never fall back to invocation.
	AuthorizeResultRead ResultAuthorizer
}

type Registry struct {
	mu      sync.RWMutex
	entries map[string]Registration
	sealed  bool
}

func NewRegistry() *Registry { return &Registry{entries: map[string]Registration{}} }

func copyDefinition(in sdk.Definition) sdk.Definition {
	in.InputSchema = append(json.RawMessage(nil), in.InputSchema...)
	in.OutputSchema = append(json.RawMessage(nil), in.OutputSchema...)
	return in
}

func (r *Registry) Register(in Registration) error {
	if in.Definition.Key == "" || in.Definition.Version == "" || in.Definition.ActionKey == "" || !json.Valid(in.Definition.InputSchema) || !json.Valid(in.Definition.OutputSchema) || in.Authorize == nil || in.Invoke == nil {
		return fmt.Errorf("tool registration requires a versioned definition, authorization and implementation")
	}
	if in.Definition.TimeoutMillis < 1 || in.Definition.TimeoutMillis > 300000 || in.Definition.MaxOutputBytes < 1 || in.Definition.MaxOutputBytes > 1048576 {
		return fmt.Errorf("invalid tool execution limits")
	}
	if _, err := schema.CompileSchema(in.Definition.InputSchema); err != nil {
		return fmt.Errorf("invalid input schema: %w", err)
	}
	if _, err := schema.CompileSchema(in.Definition.OutputSchema); err != nil {
		return fmt.Errorf("invalid output schema: %w", err)
	}
	if in.Definition.Effect != "read" && in.Definition.Effect != "write" {
		return fmt.Errorf("invalid tool effect")
	}
	if (in.Definition.Parallelism != "" && in.Definition.Parallelism != sdk.ToolParallelismIndependentRead) ||
		(in.Definition.Parallelism == sdk.ToolParallelismIndependentRead && in.Definition.Effect != "read") {
		return fmt.Errorf("invalid tool parallelism")
	}
	if in.Definition.Effect == "write" && in.Reconcile == nil {
		return fmt.Errorf("write tool %q requires reconciliation", in.Definition.Key)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("tool registry is sealed")
	}
	if _, found := r.entries[in.Definition.Key]; found {
		return fmt.Errorf("duplicate tool %q", in.Definition.Key)
	}
	in.Definition = copyDefinition(in.Definition)
	r.entries[in.Definition.Key] = in
	return nil
}

// Select freezes a registration set. Products can create several independent
// selections from the same registry; selecting one never changes another.
func (r *Registry) Select(keys []string) (*Selection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := &Selection{entries: map[string]Registration{}}
	for _, key := range keys {
		if _, duplicate := out.entries[key]; duplicate {
			return nil, fmt.Errorf("duplicate selected tool %q", key)
		}
		entry, found := r.entries[key]
		if !found {
			return nil, fmt.Errorf("unregistered tool %q", key)
		}
		entry.Definition = copyDefinition(entry.Definition)
		out.entries[key] = entry
	}
	r.sealed = true
	return out, nil
}

type Selection struct{ entries map[string]Registration }

func (s *Selection) ConversationTools(ctx context.Context, a sdk.Authority) ([]sdk.Definition, error) {
	out := []sdk.Definition{}
	for _, entry := range s.entries {
		definition := copyDefinition(entry.Definition)
		auth, err := entry.Authorize(ctx, sdk.Request{Authority: a, Definition: definition})
		if err != nil {
			return nil, err
		}
		if auth.Granted {
			out = append(out, definition)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func digest(v any) [32]byte { b, _ := json.Marshal(v); return sha256.Sum256(b) }
func denied() error {
	return &sdk.Error{Class: "forbidden", Code: "agent.conversation.tool_access_denied"}
}

func (s *Selection) entry(in sdk.Request) (Registration, error) {
	entry, found := s.entries[in.Definition.Key]
	if !found || digest(entry.Definition) != digest(in.Definition) || in.Call.Name != "" && in.Call.Name != entry.Definition.Key {
		return Registration{}, denied()
	}
	return entry, nil
}

func (s *Selection) AuthorizeConversationTool(ctx context.Context, in sdk.Request) (sdk.Authorization, error) {
	entry, err := s.entry(in)
	if err != nil {
		return sdk.Authorization{}, err
	}
	return entry.Authorize(ctx, in)
}

func (s *Selection) invoke(ctx context.Context, in sdk.Request, reconcile bool) (sdk.Result, error) {
	if in.OutcomeInspectionToken != "" {
		return sdk.Result{}, denied()
	}
	entry, err := s.entry(in)
	if err != nil {
		return sdk.Result{}, err
	}
	auth, err := entry.Authorize(ctx, in)
	if err != nil {
		return sdk.Result{}, err
	}
	if !auth.Granted || auth.ConfirmationRequired {
		return sdk.Result{}, denied()
	}
	compiled, err := schema.CompileSchema(entry.Definition.InputSchema)
	if err != nil || schema.ValidateJSON(compiled, []byte(in.Call.Arguments)) != nil {
		return sdk.Result{}, &sdk.Error{Class: "bad_request", Code: "tool.arguments_invalid"}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(entry.Definition.TimeoutMillis)*time.Millisecond)
	defer cancel()
	var result sdk.Result
	if reconcile {
		if entry.Reconcile == nil {
			return sdk.Result{}, fmt.Errorf("tool has no reconciliation operation")
		}
		result, err = entry.Reconcile(ctx, in)
	} else {
		result, err = entry.Invoke(ctx, in)
	}
	if err != nil {
		return result, err
	}
	if result.Status == "completed" {
		compiled, e := schema.CompileSchema(entry.Definition.OutputSchema)
		if e != nil || len(result.Content) > entry.Definition.MaxOutputBytes || schema.ValidateJSON(compiled, result.Content) != nil {
			return sdk.Result{Status: "uncertain", ErrorCode: "tool.output_invalid"}, nil
		}
	}
	return result, nil
}
func (s *Selection) InvokeConversationTool(ctx context.Context, in sdk.Request) (sdk.Result, error) {
	return s.invoke(ctx, in, false)
}
func (s *Selection) ReconcileConversationTool(ctx context.Context, in sdk.Request) (sdk.Result, error) {
	return s.invoke(ctx, in, true)
}
func (s *Selection) AuthorizeConversationToolResult(ctx context.Context, in sdk.Request, result sdk.Result) error {
	entry, err := s.entry(in)
	if err != nil {
		return err
	}
	auth, err := entry.Authorize(ctx, in)
	if err != nil {
		return err
	}
	if !auth.Granted {
		return denied()
	}
	if entry.AuthorizeResult != nil {
		return entry.AuthorizeResult(ctx, in, result)
	}
	return nil
}

var _ sdk.Host = (*Selection)(nil)
var _ sdk.ResultAuthorizer = (*Selection)(nil)
var _ sdk.ResultReadAuthorizer = (*Selection)(nil)

func (s *Selection) AuthorizeConversationToolResultRead(ctx context.Context, in sdk.Request, result sdk.Result) error {
	entry, err := s.entry(in)
	if err != nil {
		return err
	}
	if entry.AuthorizeResultRead == nil {
		return &sdk.Error{Class: "unavailable", Code: sdk.ResultReadUnsupportedCode}
	}
	if !in.Authority.Known || in.Authority.RuntimeID == "" || in.Authority.WorkspaceID == "" || in.Authority.UserID == "" || in.Call.Name != entry.Definition.Key || result.Status != "completed" || len(result.Content) > entry.Definition.MaxOutputBytes || in.OutcomeInspectionToken != "" {
		return denied()
	}
	input, err := schema.CompileSchema(entry.Definition.InputSchema)
	if err != nil || schema.ValidateJSON(input, []byte(in.Call.Arguments)) != nil {
		return denied()
	}
	output, err := schema.CompileSchema(entry.Definition.OutputSchema)
	if err != nil || schema.ValidateJSON(output, result.Content) != nil {
		return denied()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(entry.Definition.TimeoutMillis)*time.Millisecond)
	defer cancel()
	if err = entry.AuthorizeResultRead(ctx, in, result); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *Selection) InspectConversationToolOutcome(ctx context.Context, in sdk.Request) (sdk.Result, error) {
	entry, err := s.entry(in)
	if err != nil {
		return sdk.Result{}, err
	}
	if in.OutcomeInspectionToken == "" || in.IdempotencyKey == "" || entry.Definition.Effect != "write" || entry.InspectOutcome == nil {
		return sdk.Result{}, &sdk.Error{Class: "unavailable", Code: "agent.conversation.outcome_inspection_unavailable"}
	}
	auth, err := entry.Authorize(ctx, in)
	if err != nil {
		return sdk.Result{}, err
	}
	if !auth.Granted || auth.ConfirmationRequired {
		return sdk.Result{}, denied()
	}
	compiled, err := schema.CompileSchema(entry.Definition.InputSchema)
	if err != nil || schema.ValidateJSON(compiled, []byte(in.Call.Arguments)) != nil {
		return sdk.Result{}, denied()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(entry.Definition.TimeoutMillis)*time.Millisecond)
	defer cancel()
	out, err := entry.InspectOutcome(ctx, in)
	if err != nil {
		return sdk.Result{}, err
	}
	if out.Status == "completed" {
		compiled, e := schema.CompileSchema(entry.Definition.OutputSchema)
		if e != nil || len(out.Content) > entry.Definition.MaxOutputBytes || schema.ValidateJSON(compiled, out.Content) != nil {
			return sdk.Result{Status: "uncertain", ErrorCode: "tool.output_invalid"}, nil
		}
	}
	return out, nil
}
