// Package records adapts structured document services to selectable tools.
package records

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/domainry/domainry-knowledge-sdk/contract"
	sdk "github.com/domainry/domainry-tools-sdk"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
	"strings"
)

type Spec struct {
	Kind, Prefix, Name string
	DataSchema         json.RawMessage
	Validate           contract.Validator
}
type Adapter struct {
	Store     contract.Repository
	Spec      Spec
	Authorize tools.Authorizer
}

func Definitions(s Spec) []sdk.Definition {
	makeDef := func(op, description string, input any) sdk.Definition {
		raw, _ := json.Marshal(input)
		effect, idem := "read", "natural"
		if op == "save" {
			effect, idem = "write", "key"
		}
		parallelism := ""
		if effect == "read" {
			parallelism = sdk.ToolParallelismIndependentRead
		}
		return sdk.Definition{Key: s.Prefix + "_" + op, Version: "1", ActionKey: s.Prefix + "." + op, Description: description, InputSchema: raw, OutputSchema: json.RawMessage(`{"type":"object"}`), Effect: effect, Idempotency: idem, Parallelism: parallelism, TimeoutMillis: 15000, MaxOutputBytes: 262144}
	}
	str := func(max int) any { return map[string]any{"type": "string", "maxLength": max} }
	object := func(properties map[string]any, required ...string) any {
		return map[string]any{"type": "object", "properties": properties, "required": append([]string{}, required...), "additionalProperties": false}
	}
	return []sdk.Definition{
		makeDef("list", "List "+s.Name+" owned by the current user. Search covers title and document fields; follow next_cursor.", object(map[string]any{"query": str(255), "cursor": str(96), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 5}})),
		makeDef("read", "Read an immutable revision of "+s.Name+". Omit revision for the current head.", object(map[string]any{"id": str(96), "revision": map[string]any{"type": "integer", "minimum": 1}}, "id")),
		makeDef("save", "Create or replace "+s.Name+". Read before updating, preserve untouched fields, and use expected_revision. Product validation enforces lifecycle and acceptance. Never invent completion evidence.", object(map[string]any{"id": str(96), "expected_revision": map[string]any{"type": "integer", "minimum": 0}, "title": map[string]any{"type": "string", "minLength": 1, "maxLength": 255}, "status": str(32), "data": s.DataSchema}, "expected_revision", "title", "status", "data")),
	}
}
func (a *Adapter) Register(reg *tools.Registry) error {
	if a.Store == nil || a.Spec.Validate == nil || a.Authorize == nil {
		return fmt.Errorf("record tool adapter incomplete")
	}
	for _, d := range Definitions(a.Spec) {
		r := tools.Registration{Definition: d, Authorize: a.Authorize, Invoke: a.Invoke, Reconcile: a.Reconcile, AuthorizeResultRead: a.AuthorizeResultRead}
		if d.Effect == "write" {
			r.InspectOutcome = a.Reconcile
		}
		if err := reg.Register(r); err != nil {
			return err
		}
	}
	return nil
}
func (a *Adapter) mutation(in sdk.Request) (contract.Write, error) {
	var out contract.Write
	if err := json.Unmarshal([]byte(in.Call.Arguments), &out); err != nil {
		return out, err
	}
	if in.IdempotencyKey == "" {
		return out, fmt.Errorf("write idempotency key required")
	}
	hash := sha256.Sum256([]byte(in.IdempotencyKey))
	out.ClientID = "tool-" + hex.EncodeToString(hash[:])
	return out, nil
}
func result(v any, err error) (sdk.Result, error) {
	if err != nil {
		return sdk.Result{}, err
	}
	b, e := json.Marshal(v)
	return sdk.Result{Status: "completed", Content: b}, e
}
func (a *Adapter) Invoke(ctx context.Context, in sdk.Request) (sdk.Result, error) {
	if in.OutcomeInspectionToken != "" {
		return sdk.Result{}, fmt.Errorf("inspection cannot invoke a record mutation")
	}
	switch strings.TrimPrefix(in.Definition.Key, a.Spec.Prefix+"_") {
	case "list":
		var q struct {
			Query, Cursor string
			Limit         int
		}
		if err := json.Unmarshal([]byte(in.Call.Arguments), &q); err != nil {
			return sdk.Result{}, err
		}
		if q.Limit == 0 {
			q.Limit = 5
		}
		p, e := a.Store.List(ctx, a.Spec.Kind, q.Query, q.Cursor, q.Limit, in.Authority)
		return result(p, e)
	case "read":
		var q struct {
			ID       string
			Revision int64
		}
		if err := json.Unmarshal([]byte(in.Call.Arguments), &q); err != nil {
			return sdk.Result{}, err
		}
		r, e := a.Store.Get(ctx, a.Spec.Kind, q.ID, q.Revision, in.Authority)
		return result(r, e)
	case "save":
		w, e := a.mutation(in)
		if e != nil {
			return sdk.Result{}, e
		}
		r, e := a.Store.Save(ctx, a.Spec.Kind, w, in.Authority, a.Spec.Validate)
		out, e := result(r, e)
		out.ResourceID = r.ID
		return out, e
	}
	return sdk.Result{}, fmt.Errorf("unsupported record tool")
}
func (a *Adapter) Reconcile(ctx context.Context, in sdk.Request) (sdk.Result, error) {
	if in.Definition.Effect == "read" {
		return a.Invoke(ctx, in)
	}
	w, e := a.mutation(in)
	if e != nil {
		return sdk.Result{}, e
	}
	r, found, e := a.Store.Receipt(ctx, a.Spec.Kind, w, in.Authority)
	if e != nil {
		return sdk.Result{}, e
	}
	if !found {
		return sdk.Result{Status: "uncertain", ErrorCode: "record.receipt_missing"}, nil
	}
	out, e := result(r, nil)
	out.ResourceID = r.ID
	return out, e
}
