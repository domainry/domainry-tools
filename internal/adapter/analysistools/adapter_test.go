package analysistools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	model "github.com/domainry/domainry-report-sdk/model"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools-sdk/schema"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

type sourceFixture struct {
	identity, version                      string
	denied, validationOnly, partial, large bool
	runs, checks, catalogs                 int
	request                                model.AnalysisRequest
	proofs                                 map[string]string
}

func (s *sourceFixture) BusinessSourceIdentity() string { return s.identity }
func (s *sourceFixture) AnalysisCatalog(ctx context.Context, q model.AnalysisCatalogRequest, a sdk.Authority) (model.AnalysisCatalog, error) {
	if err := ctx.Err(); err != nil {
		return model.AnalysisCatalog{}, err
	}
	if s.denied || a.UserID != "alice" {
		return model.AnalysisCatalog{}, failure("forbidden", "denied")
	}
	s.catalogs++
	return model.AnalysisCatalog{Datasets: []model.AnalysisDataset{{Key: "sales", Kind: "business_object", Version: s.version, Columns: []model.AnalysisColumn{{Key: "amount", Type: "currency", Unit: "CNY"}}}}}, nil
}
func (s *sourceFixture) RunAnalysis(ctx context.Context, q model.AnalysisRequest, a sdk.Authority) (model.AnalysisResult, error) {
	if s.denied || a.UserID != "alice" {
		return model.AnalysisResult{}, failure("forbidden", "denied")
	}
	if err := ctx.Err(); err != nil {
		return model.AnalysisResult{}, err
	}
	if s.validationOnly {
		return model.AnalysisResult{}, nil
	}
	s.runs++
	s.request = q
	value := "9007199254740993.20"
	if s.large {
		value = strings.Repeat("x", 1048577)
	}
	out := model.AnalysisResult{Spec: q, Columns: []model.AnalysisColumn{{Key: "total", Type: "decimal", Unit: "CNY"}, {Key: "undefined", Type: "decimal", Unit: "CNY"}}, Rows: []model.AnalysisRow{{Values: map[string]*string{"total": &value, "undefined": nil}, InputCounts: map[string]string{"dataset": "12001"}, NonNullCounts: map[string]string{"total": "12000"}, Issues: []model.AnalysisCellIssue{{Column: "undefined", Code: "zero_baseline"}}}}, Methods: []model.AnalysisMethod{{Column: "total", Method: "sum"}}, Visualization: model.AnalysisVisualization{Chart: nil, OmittedReason: "single_value_or_multiple_dimensions"}, Coverage: model.AnalysisCoverage{Complete: !s.partial, Truncated: false, ReturnedRows: 1, RequestedMaxRows: q.MaxRows, Missing: []model.AnalysisMissing{{Column: "undefined", Code: "null_value", Count: "1"}}}, References: []model.AnalysisReference{{Kind: "business_object", ID: q.DatasetKey, Version: s.version}}, Source: model.AnalysisSource{DatasetKey: q.DatasetKey, DefinitionVersion: s.version, DataVersion: "data-1", QueriedAt: "2026-09-12T00:00:00Z", InputCounts: map[string]string{"dataset": "12001"}, Complete: !s.partial, Proof: "owner-proof"}}
	s.proofs[digest(q)] = digest(out)
	return out, nil
}
func (s *sourceFixture) AuthorizeAnalysisResult(_ context.Context, in model.AnalysisResultAuthorization, a sdk.Authority) error {
	s.checks++
	if s.denied || a.UserID != "alice" || s.proofs[digest(in.Request)] != digest(in.Result) || in.Result.Source.DefinitionVersion != s.version {
		return failure("forbidden", "invalid_proof")
	}
	return nil
}
func fixture(t *testing.T) (*Adapter, *tools.Selection, *sourceFixture, sdk.Request, *bool) {
	t.Helper()
	s := &sourceFixture{identity: "current-host", version: "v1", proofs: map[string]string{}}
	grant := true
	a := &Adapter{Source: func() Source { return s }, Authorize: func(context.Context, sdk.Request) (sdk.Authorization, error) {
		return sdk.Authorization{Granted: grant, Revision: "identity-1"}, nil
	}}
	registry := tools.NewRegistry()
	if err := a.Register(registry); err != nil {
		t.Fatal(err)
	}
	h, err := registry.Select([]string{Key})
	if err != nil {
		t.Fatal(err)
	}
	r := sdk.Request{Authority: sdk.Authority{Known: true, RuntimeID: "r", WorkspaceID: "w", UserID: "alice"}, Definition: Definitions()[0], Call: sdk.Call{Name: Key, ID: "call-1", Arguments: `{"operation":"run","spec":{"dataset_key":"sales","filters":[{"field":"amount","operator":"ge","values":[9007199254740993]}],"measures":[{"key":"total","field":"amount","function":"sum"}]}}`}}
	return a, h, s, r, &grant
}

func TestAnalysisToolPreservesOwnerEvidenceAndRechecksWithoutReexecution(t *testing.T) {
	_, h, s, r, _ := fixture(t)
	defs, err := h.ConversationTools(t.Context(), r.Authority)
	if err != nil || len(defs) != 1 || s.catalogs != 1 {
		t.Fatal(defs, err)
	}
	out, err := h.InvokeConversationTool(t.Context(), r)
	if err != nil || out.Status != "completed" {
		t.Fatal(out, err)
	}
	if s.runs != 1 || s.request.Filters[0].Values[0] != json.Number("9007199254740993") {
		t.Fatal(s.request, s.runs)
	}
	for _, text := range []string{`9007199254740993.20`, `"undefined":null`, `"dataset":"12001"`, `"total":"12000"`, `"zero_baseline"`, `"CNY"`, `"complete":true`, `"truncated":false`, `"omitted_reason":"single_value_or_multiple_dimensions"`, `"kind":"business_object"`} {
		if !strings.Contains(string(out.Content), text) {
			t.Fatal("lost owner fact", text, string(out.Content))
		}
	}
	if err := h.AuthorizeConversationToolResult(t.Context(), r, out); err != nil {
		t.Fatal(err)
	}
	if s.runs != 1 || s.catalogs != 1 || s.checks != 2 {
		t.Fatal("reexecuted data or repeated catalog", s.runs, s.catalogs, s.checks)
	}
	s.denied = true
	if err := h.AuthorizeConversationToolResult(t.Context(), r, out); err == nil {
		t.Fatal("revoked result accepted")
	}
	if _, err := h.InvokeConversationTool(t.Context(), r); err == nil || s.runs != 1 {
		t.Fatal("revoked source executed", err)
	}
}
func TestAnalysisToolRejectsMissingPermissionPartialAndValidationOnly(t *testing.T) {
	for _, state := range []string{"missing", "typed_nil", "identity", "permission", "owner", "partial", "validation", "large", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			a, h, s, r, grant := fixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch state {
			case "missing":
				a.Source = func() Source { return nil }
			case "typed_nil":
				a.Source = func() Source { var value *sourceFixture; return value }
			case "identity":
				s.identity = ""
			case "permission":
				*grant = false
			case "owner":
				s.denied = true
			case "partial":
				s.partial = true
			case "validation":
				s.validationOnly = true
			case "large":
				s.large = true
			case "cancelled":
				cancel()
			}
			out, err := h.InvokeConversationTool(ctx, r)
			if state == "large" {
				if err != nil || out.Status != "failed" || out.ErrorCode != "analysis.result_too_large_narrow_spec" {
					t.Fatal("size failure lost", out, err)
				}
			} else if err == nil || out.Status == "completed" {
				t.Fatal("non-execution or partial result became success", out, err)
			}
			if state == "large" && s.checks != 0 {
				t.Fatal("oversize result sent to saved-result authorization")
			}
		})
	}
}

func TestAnalysisFixedFailureRevalidationDoesNotExposeOwnerErrors(t *testing.T) {
	a, h, s, r, _ := fixture(t)
	s.large = true
	out, err := h.InvokeConversationTool(t.Context(), r)
	if err != nil || out.Status != "failed" {
		t.Fatal(out, err)
	}
	if err := h.AuthorizeConversationToolResult(t.Context(), r, out); err != nil {
		t.Fatal(err)
	}
	if s.checks != 0 || s.runs != 1 {
		t.Fatal("failure revalidation reran analysis")
	}
	out.Content = json.RawMessage(`{"error":"analysis.result_too_large_narrow_spec","recovery":"secret rows"}`)
	if err := a.AuthorizeResult(t.Context(), r, out); err == nil {
		t.Fatal("forged failure content accepted")
	}
	if code := recoverableCode(&sdk.Error{Code: "driver.secret", Message: "SELECT secret"}); code != "" {
		t.Fatal("unknown owner error exposed", code)
	}
}
func TestAnalysisToolRejectsUntrustedInputAndSavedContent(t *testing.T) {
	for _, raw := range []string{`null`, `{"operation":"run"}`, `{"operation":"run","spec":{"dataset_key":"sales","sql":"SELECT secret"}}`, `{"operation":"run","spec":{"dataset_key":"sales","filters":[{"field":"amount","operator":"eq","values":[{"token":"secret"}]}]}}`, `{"operation":"run","spec":{"dataset_key":"sales","calculations":[{"key":"x","scale":2,"expression":{"code":"fetch(secret)"}}]}}`, `{"operation":"run","spec":{"dataset_key":"sales","max_rows":501}}`, `{"operation":"catalog","spec":{}}`, `{"operation":"catalog","authority":{"user_id":"admin"}}`} {
		_, h, s, r, _ := fixture(t)
		r.Call.Arguments = raw
		if _, err := h.InvokeConversationTool(t.Context(), r); err == nil || s.runs != 0 {
			t.Fatal("unsafe input executed", raw, err)
		}
	}
	for _, change := range []string{"request", "host", "user", "version", "status", "completion", "rows", "method", "unit", "coverage", "chart", "reference", "proof", "source", "catalog", "extra"} {
		t.Run(change, func(t *testing.T) {
			_, h, s, r, _ := fixture(t)
			out, err := h.InvokeConversationTool(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			var envelope output
			if err := decode(out.Content, &envelope); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "request":
				r.Call.Arguments = `{"operation":"run","spec":{"dataset_key":"sales","measures":[{"key":"rows","function":"count"}]}}`
			case "host":
				s.identity = "another-host"
			case "user":
				r.Authority.UserID = "bob"
			case "version":
				s.version = "v2"
			case "status":
				out.Status = "failed"
			case "completion":
				out.Completion = "accepted"
			case "rows":
				value := "0"
				envelope.Result.Rows[0].Values["total"] = &value
			case "method":
				envelope.Result.Methods[0].Method = "sample"
			case "unit":
				envelope.Result.Columns[0].Unit = "USD"
			case "coverage":
				envelope.Result.Coverage.Truncated = true
			case "chart":
				envelope.Result.Visualization.OmittedReason = ""
			case "reference":
				envelope.Result.References = nil
			case "proof":
				envelope.Result.Source.Proof = "forged"
			case "source":
				envelope.Result.Source.InputCounts["dataset"] = "1"
			case "catalog":
				envelope.Catalog = &model.AnalysisCatalog{}
			}
			out.Content, _ = json.Marshal(envelope)
			if change == "extra" {
				out.Content = append(out.Content[:len(out.Content)-1], []byte(`,"sql":"secret"}`)...)
			}
			if err := h.AuthorizeConversationToolResult(t.Context(), r, out); err == nil {
				t.Fatal("altered result accepted")
			}
			if s.runs != 1 {
				t.Fatal("saved result executed again")
			}
		})
	}
}
func TestAnalysisToolCatalogAndRecursiveProtocolShapes(t *testing.T) {
	_, h, s, r, _ := fixture(t)
	r.Call.Arguments = `{"operation":"catalog"}`
	out, err := h.InvokeConversationTool(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.AuthorizeConversationToolResult(t.Context(), r, out); err != nil {
		t.Fatal(err)
	}
	s.version = "v2"
	if err := h.AuthorizeConversationToolResult(t.Context(), r, out); err == nil {
		t.Fatal("stale catalog accepted")
	}
	if s.runs != 0 {
		t.Fatal("catalog executed data")
	}
	compiled, err := schema.CompileSchema(Definitions()[0].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"aggregate", "compare", "trend", "table"} {
		payload := `{"operation":"run","spec":{"dataset_key":"sales","mode":"` + mode + `","filters":[{"all":[{"any":[{"field":"amount","operator":"ge","values":[1]}]}]}],"calculations":[{"key":"adjusted","scale":2,"expression":{"operator":"add","arguments":[{"reference":"total"},{"operator":"negate","arguments":[{"decimal":"0.01"}]}]}}]}}`
		if err := schema.ValidateJSON(compiled, []byte(payload)); err != nil {
			t.Fatal("recursive closed schema rejected valid shape", mode, err)
		}
	}
}
