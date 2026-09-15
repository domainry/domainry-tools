package records

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/domainry/domainry-knowledge/contract"
	sdk "github.com/domainry/domainry-tools-sdk"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

type recordReadStore struct {
	original                     contract.Record
	owner                        sdk.Authority
	gets, receipts, lists, saves int
	missingReceipt               bool
	afterGet                     func()
	afterReceipt                 func()
}

func (s *recordReadStore) Get(_ context.Context, kind, id string, revision int64, a sdk.Authority) (contract.Record, error) {
	s.gets++
	if !a.Known || a.RuntimeID != s.owner.RuntimeID || a.WorkspaceID != s.owner.WorkspaceID || a.UserID != s.owner.UserID || kind != s.original.Kind || id != s.original.ID || revision != s.original.Revision {
		return contract.Record{}, fmt.Errorf("record outside immutable owner scope")
	}
	if s.afterGet != nil {
		s.afterGet()
	}
	return s.original, nil
}
func (s *recordReadStore) List(context.Context, string, string, string, int, sdk.Authority) (contract.Page, error) {
	s.lists++
	return contract.Page{}, fmt.Errorf("result read must not relist")
}
func (s *recordReadStore) Save(context.Context, string, contract.Write, sdk.Authority, contract.Validator) (contract.Record, error) {
	s.saves++
	return contract.Record{}, fmt.Errorf("result read must not save")
}
func (s *recordReadStore) Receipt(_ context.Context, kind string, w contract.Write, a sdk.Authority) (contract.Record, bool, error) {
	s.receipts++
	if s.afterReceipt != nil {
		s.afterReceipt()
	}
	if s.missingReceipt {
		return contract.Record{}, false, nil
	}
	if kind != s.original.Kind || a.UserID != s.owner.UserID || a.RuntimeID != s.owner.RuntimeID || a.WorkspaceID != s.owner.WorkspaceID || w.Title != s.original.Title || w.ClientID == "" {
		return contract.Record{}, false, fmt.Errorf("wrong original write receipt")
	}
	return s.original, true, nil
}

func recordReadFixture(t *testing.T) (*Adapter, *recordReadStore, *tools.Selection, *bool, *bool) {
	t.Helper()
	store := &recordReadStore{original: contract.Record{ID: "rec_original", Kind: "requirements", Title: "Original", Status: "draft", Revision: 1, Data: json.RawMessage(`{"amount":9007199254740993}`), CreatedAt: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)}, owner: sdk.Authority{Known: true, RuntimeID: "runtime", WorkspaceID: "workspace", UserID: "owner", RoleKey: "old_writer"}}
	readable, originalReadable := true, true
	adapter := &Adapter{Store: store, Spec: Spec{Kind: "requirements", Prefix: "requirements", Name: "Requirements", DataSchema: json.RawMessage(`{"type":"object"}`), Validate: func(_, _ *contract.Record) error {
		t.Error("result read invoked product mutation validation")
		return nil
	}}, Authorize: func(_ context.Context, r sdk.Request) (sdk.Authorization, error) {
		if r.Definition.Effect == "write" {
			return sdk.Authorization{}, nil
		}
		if r.Definition.Key != "requirements_read" && r.Definition.Key != "requirements_list" {
			t.Error("unknown reading permission", r.Definition.Key)
		}
		if r.Confirmation != nil || r.ConfirmationID != "" || r.IdempotencyKey != "" || r.LeaseOwner != "" || r.Fence != 0 || r.OutcomeInspectionToken != "" || r.ResultProducer != nil {
			t.Error("reading received execution credentials")
		}
		return sdk.Authorization{Granted: readable && (r.Authority.RoleKey != "old_writer" || originalReadable)}, nil
	}}
	registry := tools.NewRegistry()
	if err := adapter.Register(registry); err != nil {
		t.Fatal(err)
	}
	host, err := registry.Select([]string{"requirements_list", "requirements_read", "requirements_save"})
	if err != nil {
		t.Fatal(err)
	}
	return adapter, store, host, &readable, &originalReadable
}

func recordReadRequest(a sdk.Authority, key, args string, adapter *Adapter) sdk.Request {
	var definition sdk.Definition
	for _, d := range Definitions(adapter.Spec) {
		if d.Key == key {
			definition = d
		}
	}
	return sdk.Request{Authority: a, ConversationID: "conversation", RunID: "run", Step: 1, Definition: definition, Call: sdk.Call{ID: "original", Name: key, Arguments: args}, IdempotencyKey: "original-save"}
}

func TestStructuredRecordResultReadUsesImmutableVersionsAndReadPermissionOnly(t *testing.T) {
	for _, key := range []string{"requirements_list", "requirements_read", "requirements_save"} {
		t.Run(key, func(t *testing.T) {
			adapter, store, host, readable, originalReadable := recordReadFixture(t)
			reader := store.owner
			reader.RoleKey = "current_reader"
			args := `{"id":"rec_original"}`
			var value any = store.original
			resource := ""
			if key == "requirements_list" {
				args = `{"limit":1}`
				value = contract.Page{Items: []contract.Record{store.original}, Complete: true}
			}
			if key == "requirements_save" {
				args = `{"expected_revision":0,"title":"Original","status":"draft","data":{"amount":9007199254740993}}`
				resource = store.original.ID
			}
			request := recordReadRequest(reader, key, args, adapter)
			request.ResultProducer = &store.owner
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			out := sdk.Result{Status: "completed", Content: raw, ResourceID: resource}
			before := string(out.Content)
			if err := host.AuthorizeConversationToolResultRead(t.Context(), request, out); err != nil {
				t.Fatal("current reader cannot read original result after write withdrawal", err)
			}
			if string(out.Content) != before || store.saves != 0 || store.lists != 0 || store.gets == 0 {
				t.Fatal("read replaced the immutable result or replayed an operation")
			}
			if key == "requirements_save" {
				if _, err := host.InvokeConversationTool(t.Context(), request); err == nil {
					t.Fatal("reading granted save execution")
				}
				if _, err := host.ReconcileConversationTool(t.Context(), request); err == nil {
					t.Fatal("reading granted save reconciliation")
				}
				store.missingReceipt = true
				if err := host.AuthorizeConversationToolResultRead(t.Context(), request, out); err == nil {
					t.Fatal("missing original ledger retried or passed")
				}
				store.missingReceipt = false
			}
			*readable = false
			if err := host.AuthorizeConversationToolResultRead(t.Context(), request, out); err == nil {
				t.Fatal("reader permission withdrawal ignored")
			}
			*readable = true
			*originalReadable = false
			if err := host.AuthorizeConversationToolResultRead(t.Context(), request, out); err == nil {
				t.Fatal("original provider permission withdrawal ignored")
			}
			*originalReadable = true
			store.afterGet = func() { *readable = false }
			if err := host.AuthorizeConversationToolResultRead(t.Context(), request, out); err == nil {
				t.Fatal("withdrawal during original version lookup ignored")
			}
		})
	}
}

func TestStructuredRecordResultReadRejectsForeignOwnersDefinitionsAndChangedResults(t *testing.T) {
	adapter, store, host, _, _ := recordReadFixture(t)
	request := recordReadRequest(store.owner, "requirements_read", `{"id":"rec_original","revision":1}`, adapter)
	raw, _ := json.Marshal(store.original)
	out := sdk.Result{Status: "completed", Content: raw}
	for _, test := range []struct {
		name   string
		change func(*sdk.Request, *sdk.Result)
	}{
		{"foreign reader", func(r *sdk.Request, _ *sdk.Result) { r.Authority.UserID = "other"; r.ResultProducer = &store.owner }},
		{"foreign original provider", func(r *sdk.Request, _ *sdk.Result) { p := store.owner; p.UserID = "other"; r.ResultProducer = &p }},
		{"foreign workspace", func(r *sdk.Request, _ *sdk.Result) { r.Authority.WorkspaceID = "other" }},
		{"foreign runtime", func(r *sdk.Request, _ *sdk.Result) { r.Authority.RuntimeID = "other" }},
		{"changed definition", func(r *sdk.Request, _ *sdk.Result) { r.Definition.Version = "2" }},
		{"changed original request", func(r *sdk.Request, _ *sdk.Result) { r.Call.Arguments = `{"id":"other"}` }},
		{"changed original version", func(r *sdk.Request, _ *sdk.Result) { r.Call.Arguments = `{"id":"rec_original","revision":2}` }},
		{"changed payload", func(_ *sdk.Request, o *sdk.Result) {
			v := store.original
			v.Title = "Changed"
			o.Content, _ = json.Marshal(v)
		}},
		{"unexpected resource", func(_ *sdk.Request, o *sdk.Result) { o.ResourceID = store.original.ID }},
		{"unknown original field", func(_ *sdk.Request, o *sdk.Result) {
			o.Content = json.RawMessage(`{"id":"rec_original","kind":"requirements","title":"Original","revision":1,"data":{},"unexpected":"secret"}`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, o := request, out
			test.change(&r, &o)
			if err := host.AuthorizeConversationToolResultRead(t.Context(), r, o); err == nil {
				t.Fatal("invalid source result accepted")
			}
		})
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := host.AuthorizeConversationToolResultRead(cancelled, request, out); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled original lookup ignored", err)
	}
}

func TestStructuredRecordSavedPagesRemainHistoricalAndValidateOriginalBounds(t *testing.T) {
	adapter, store, host, readable, _ := recordReadFixture(t)
	request := recordReadRequest(store.owner, "requirements_list", `{"query":"Original","limit":1}`, adapter)
	for _, test := range []struct {
		name string
		page contract.Page
		want bool
	}{
		{"original incomplete cursor", contract.Page{Items: []contract.Record{store.original}, NextCursor: store.original.ID, Complete: false}, true},
		{"original complete page", contract.Page{Items: []contract.Record{store.original}, Complete: true}, true},
		{"wrong cursor", contract.Page{Items: []contract.Record{store.original}, NextCursor: "other", Complete: false}, false},
		{"complete page with cursor", contract.Page{Items: []contract.Record{store.original}, NextCursor: store.original.ID, Complete: true}, false},
		{"incomplete empty page", contract.Page{Items: []contract.Record{}, Complete: false}, false},
		{"too many original rows", contract.Page{Items: []contract.Record{store.original, store.original}, Complete: true}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, _ := json.Marshal(test.page)
			err := host.AuthorizeConversationToolResultRead(t.Context(), request, sdk.Result{Status: "completed", Content: raw})
			if (err == nil) != test.want {
				t.Fatal("original page bounds not honored", err)
			}
		})
	}
	empty := contract.Page{Items: []contract.Record{}, Complete: true}
	raw, _ := json.Marshal(empty)
	beforeGets := store.gets
	if err := host.AuthorizeConversationToolResultRead(t.Context(), request, sdk.Result{Status: "completed", Content: raw}); err != nil || store.gets != beforeGets || store.lists != 0 {
		t.Fatal("empty original page was relisted or rejected", err)
	}
	*readable = false
	if err := host.AuthorizeConversationToolResultRead(t.Context(), request, sdk.Result{Status: "completed", Content: raw}); err == nil {
		t.Fatal("empty page retained revoked metadata access")
	}
}

func TestStructuredRecordSaveReceiptRechecksReaderAfterOriginalOwnerLedgerLookup(t *testing.T) {
	adapter, store, host, readable, _ := recordReadFixture(t)
	reader := store.owner
	reader.RoleKey = "current_reader"
	request := recordReadRequest(reader, "requirements_save", `{"expected_revision":0,"title":"Original","status":"draft","data":{"amount":9007199254740993}}`, adapter)
	request.ResultProducer = &store.owner
	raw, _ := json.Marshal(store.original)
	store.afterReceipt = func() { *readable = false }
	if err := host.AuthorizeConversationToolResultRead(t.Context(), request, sdk.Result{Status: "completed", Content: raw, ResourceID: store.original.ID}); err == nil || store.receipts != 1 || store.saves != 0 {
		t.Fatal("reader withdrawal during original ledger IO passed", err)
	}
}
