package reporttools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
	sdk "github.com/domainry/domainry-tools-sdk"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

type sourceFixture struct {
	identity        string
	denied          bool
	validationOnly  bool
	failQuery       bool
	large           bool
	reads, checks   int
	catalogRequests int
	queries         []reportmodel.ReportObjectSQLRequest
	proofs          map[string]string
	definition      string
}

func (s *sourceFixture) BusinessSourceIdentity() string { return s.identity }
func (s *sourceFixture) ReportCatalog(ctx context.Context, q reportmodel.ReportCatalogRequest, a sdk.Authority) (reportmodel.ReportCatalog, error) {
	if q.Page.PageSize > 0 {
		s.catalogRequests++
	}
	if err := ctx.Err(); err != nil {
		return reportmodel.ReportCatalog{}, err
	}
	if s.denied || a.UserID != "alice" {
		return reportmodel.ReportCatalog{}, failure("forbidden", "denied")
	}
	return reportmodel.ReportCatalog{Reports: []reportmodel.ReportCatalogEntry{{Key: "sales", Name: "Sales", DefinitionVersion: s.definition, RowLimit: 100, Parameters: []reportmodel.ReportObjectSQLParameter{{Key: "minimum", Type: "integer", Default: json.Number("9007199254740993")}}}}}, nil
}
func (s *sourceFixture) QueryReport(ctx context.Context, q reportmodel.ReportObjectSQLRequest, a sdk.Authority) (reportmodel.ReportQueryResult, error) {
	if _, err := s.ReportCatalog(ctx, reportmodel.ReportCatalogRequest{}, a); err != nil {
		return reportmodel.ReportQueryResult{}, err
	}
	if s.failQuery {
		return reportmodel.ReportQueryResult{}, failure("unavailable", "query_failed")
	}
	if s.validationOnly {
		return reportmodel.ReportQueryResult{}, nil
	}
	s.reads++
	s.queries = append(s.queries, q)
	value := "9007199254740993.20"
	if s.large {
		value = strings.Repeat("X", 300000)
	}
	out := reportmodel.ReportQueryResult{Summary: reportmodel.ReportSummary{Key: q.ReportKey, Name: "Sales", ExecutionMode: "object_sql_v1", Rows: []reportmodel.ReportResultRow{{Measures: map[string]string{"amount": value}}}, RowCount: 1, PageSize: q.Page.PageSize, Truncated: true, NextCursor: "owner-next", Total: 2, TotalSemantics: "at_least"}, Source: reportmodel.ReportQuerySource{ReportKey: q.ReportKey, DefinitionVersion: s.definition, DataVersion: "v1", QueriedAt: "2026-09-12T00:00:00Z", RowLimit: 100}}
	out.Source.Proof = "fixture-proof"
	s.proofs[digest(q)] = digest(out)
	return out, nil
}
func (s *sourceFixture) AuthorizeReportResult(ctx context.Context, in reportmodel.ReportQueryResultAuthorization, a sdk.Authority) error {
	s.checks++
	if _, err := s.ReportCatalog(ctx, reportmodel.ReportCatalogRequest{}, a); err != nil {
		return err
	}
	if s.proofs[digest(in.Query)] != digest(in.Result) {
		return failure("forbidden", "invalid_proof")
	}
	return nil
}
func toolFixture(t *testing.T) (*Adapter, *tools.Selection, *sourceFixture, sdk.Request, *bool) {
	t.Helper()
	s := &sourceFixture{identity: "runtime-report-source", definition: "definition-1", proofs: map[string]string{}}
	granted := true
	a := &Adapter{Source: func() Source { return s }, Authorize: func(context.Context, sdk.Request) (sdk.Authorization, error) {
		return sdk.Authorization{Granted: granted, Revision: "identity-v1"}, nil
	}}
	r := tools.NewRegistry()
	if err := a.Register(r); err != nil {
		t.Fatal(err)
	}
	selected, err := r.Select([]string{Key})
	if err != nil {
		t.Fatal(err)
	}
	in := sdk.Request{Authority: sdk.Authority{Known: true, RuntimeID: "r1", WorkspaceID: "w1", UserID: "alice"}, Definition: Definitions()[0], Call: sdk.Call{ID: "call-1", Name: Key, Arguments: `{"operation":"query","report_key":"sales","parameters":{"minimum":9007199254740993},"page_size":1}`}}
	return a, selected, s, in, &granted
}
func TestReportToolExecutesOwnerAndRevalidatesSavedRowsWithoutQuery(t *testing.T) {
	_, h, s, in, _ := toolFixture(t)
	definitions, err := h.ConversationTools(t.Context(), in.Authority)
	if err != nil || len(definitions) != 1 || s.catalogRequests != 1 {
		t.Fatal("tool discovery must check the current catalog", definitions, err)
	}
	out, err := h.InvokeConversationTool(t.Context(), in)
	if err != nil || out.Status != "completed" || s.reads != 1 || s.queries[0].Parameters["minimum"] != json.Number("9007199254740993") {
		t.Fatal(out, err, s.queries)
	}
	if !strings.Contains(string(out.Content), `"complete":false`) || !strings.Contains(string(out.Content), `9007199254740993.20`) {
		t.Fatal("lost completeness or precision", string(out.Content))
	}
	if err := h.AuthorizeConversationToolResult(t.Context(), in, out); err != nil {
		t.Fatal(err)
	}
	if s.reads != 1 || s.checks != 2 {
		t.Fatal("source authorization executed report", s.reads, s.checks)
	}
	if s.catalogRequests != 1 {
		t.Fatal("concrete owner authorization redundantly rediscovered the full catalog", s.catalogRequests)
	}
	s.denied = true
	if err := h.AuthorizeConversationToolResult(t.Context(), in, out); err == nil {
		t.Fatal("old result visible after revocation")
	}
	if _, err := h.InvokeConversationTool(t.Context(), in); err == nil {
		t.Fatal("revoked query executed")
	}
	if s.reads != 1 {
		t.Fatal("revocation reached query")
	}
}
func TestReportToolCatalogRechecksOwnerMetadata(t *testing.T) {
	_, h, s, in, _ := toolFixture(t)
	in.Call.Arguments = `{"operation":"catalog"}`
	out, err := h.InvokeConversationTool(t.Context(), in)
	if err != nil || out.Status != "completed" {
		t.Fatal(out, err)
	}
	if err := h.AuthorizeConversationToolResult(t.Context(), in, out); err != nil {
		t.Fatal(err)
	}
	if s.reads != 0 {
		t.Fatal("catalog executed query")
	}
	s.definition = "changed"
	if err := h.AuthorizeConversationToolResult(t.Context(), in, out); err == nil {
		t.Fatal("changed catalog accepted")
	}
}
func TestReportToolUnconfiguredUnpermittedAndValidationOnlyFailClosed(t *testing.T) {
	for _, state := range []string{"missing", "typed-nil", "identity", "permission", "owner", "validation", "execution", "oversized"} {
		t.Run(state, func(t *testing.T) {
			a, h, s, in, granted := toolFixture(t)
			switch state {
			case "missing":
				a.Source = func() Source { return nil }
			case "typed-nil":
				a.Source = func() Source { var missing *sourceFixture; return missing }
			case "identity":
				s.identity = ""
			case "permission":
				*granted = false
			case "owner":
				s.denied = true
			case "validation":
				s.validationOnly = true
			case "execution":
				s.failQuery = true
			case "oversized":
				s.large = true
			}
			out, err := h.InvokeConversationTool(t.Context(), in)
			if err == nil || out.Status == "completed" {
				t.Fatal("non-execution treated as success", out, err)
			}
			if state != "oversized" && s.reads != 0 {
				t.Fatal("invalid invocation reached executor")
			}
		})
	}
}
func TestReportToolRejectsUntrustedInputsBeforeOwnerExecution(t *testing.T) {
	for _, raw := range []string{`null`, `{"operation":"query"}`, `{"operation":"query","report_key":"sales","sql":"SELECT secret"}`, `{"operation":"query","report_key":"sales","authority":{"user_id":"other"}}`, `{"operation":"query","report_key":"sales","parameters":{"minimum":[]}}`, `{"operation":"catalog","parameters":{}}`, `{"operation":"query","report_key":"sales","page_size":51}`, `{"operation":"catalog","page_size":0}`, `{"operation":"query","report_key":"sales"} {}`} {
		_, h, s, in, _ := toolFixture(t)
		in.Call.Arguments = raw
		if _, err := h.InvokeConversationTool(t.Context(), in); err == nil {
			t.Fatal("accepted", raw)
		}
		if s.reads != 0 {
			t.Fatal("invalid input executed")
		}
	}
}
func TestReportToolRejectsChangedSavedEnvelopeAndContent(t *testing.T) {
	for _, change := range []string{"status", "completion", "resource", "request", "host", "user", "row", "source", "extra", "operation", "catalog"} {
		t.Run(change, func(t *testing.T) {
			_, h, s, in, _ := toolFixture(t)
			out, err := h.InvokeConversationTool(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			var content map[string]any
			if err := json.Unmarshal(out.Content, &content); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "status":
				out.Status = "failed"
			case "completion":
				out.Completion = "accepted"
			case "resource":
				out.ResourceID = "another"
			case "request":
				in.Call.Arguments = `{"operation":"query","report_key":"sales","page_size":2}`
			case "host":
				s.identity = "other-runtime"
			case "user":
				in.Authority.UserID = "other-user"
			case "row":
				content["result"].(map[string]any)["summary"].(map[string]any)["name"] = "Changed"
			case "source":
				content["result"].(map[string]any)["source"].(map[string]any)["complete"] = true
			case "extra":
				content["sql"] = "SELECT secret"
			case "operation":
				content["operation"] = "catalog"
			case "catalog":
				content["catalog"] = map[string]any{}
			}
			out.Content, _ = json.Marshal(content)
			if err := h.AuthorizeConversationToolResult(t.Context(), in, out); err == nil {
				t.Fatal("changed result accepted")
			}
			if s.reads != 1 {
				t.Fatal("result validation reexecuted query")
			}
		})
	}
}
