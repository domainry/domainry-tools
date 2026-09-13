// Package analysistools adapts Report-owned analyses to the neutral tool port.
// It has no Agent, Runtime, database or sibling tool implementation dependency.
package analysistools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	reportsdk "github.com/domainry/domainry-report-sdk"
	model "github.com/domainry/domainry-report-sdk/model"
	sdk "github.com/domainry/domainry-tools-sdk"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

const Key = sdk.AnalysisRunToolKey

func Definitions() []sdk.Definition { return sdk.AnalysisDefinitions() }

type Source interface {
	BusinessSourceIdentity() string
	AnalysisCatalog(context.Context, model.AnalysisCatalogRequest, sdk.Authority) (model.AnalysisCatalog, error)
	RunAnalysis(context.Context, model.AnalysisRequest, sdk.Authority) (model.AnalysisResult, error)
	AuthorizeAnalysisResult(context.Context, model.AnalysisResultAuthorization, sdk.Authority) error
}

type Adapter struct {
	Source    func() Source
	Authorize tools.Authorizer
}

func (a *Adapter) source() Source {
	if a == nil || a.Source == nil {
		return nil
	}
	source := a.Source()
	if source == nil || reflect.ValueOf(source).Kind() == reflect.Pointer && reflect.ValueOf(source).IsNil() || strings.TrimSpace(source.BusinessSourceIdentity()) == "" {
		return nil
	}
	return source
}

func (a *Adapter) Register(registry *tools.Registry) error {
	if a == nil || a.Source == nil || a.Authorize == nil {
		return fmt.Errorf("analysis tool host configuration is incomplete")
	}
	return registry.Register(tools.Registration{Definition: Definitions()[0], Authorize: a.authorize, Invoke: a.Invoke, Reconcile: a.Invoke, AuthorizeResult: a.AuthorizeResult, AuthorizeResultRead: a.AuthorizeResultRead})
}

func (a *Adapter) authorize(ctx context.Context, r sdk.Request) (sdk.Authorization, error) {
	if a == nil || a.Authorize == nil {
		return sdk.Authorization{}, nil
	}
	auth, err := a.Authorize(ctx, r)
	if err != nil || !auth.Granted {
		return auth, err
	}
	if r.Call.Name != "" {
		auth.Granted = r.Authority.Known && r.Authority.RuntimeID != "" && r.Authority.WorkspaceID != "" && r.Authority.UserID != "" && a.source() != nil
		return auth, nil
	}
	ready, err := a.ConversationToolAvailable(ctx, r.Authority, r.Definition.Key)
	auth.Granted = auth.Granted && ready
	return auth, err
}

func (a *Adapter) ConversationToolAvailable(ctx context.Context, authority sdk.Authority, key string) (bool, error) {
	if key != Key || !authority.Known || authority.RuntimeID == "" || authority.WorkspaceID == "" || authority.UserID == "" {
		return false, nil
	}
	source := a.source()
	if source == nil {
		return false, nil
	}
	catalog, err := source.AnalysisCatalog(ctx, model.AnalysisCatalogRequest{Page: model.ReportPageRequest{PageSize: 1}}, authority)
	var tool *sdk.Error
	if errors.As(err, &tool) && (tool.Class == "forbidden" || tool.Class == "not_found" || tool.Class == "unavailable") {
		return false, nil
	}
	var report *reportsdk.Error
	if errors.As(err, &report) && (report.StatusCode == 401 || report.StatusCode == 403 || report.StatusCode == 404 || report.StatusCode == 503 || report.Code == "backend.report.analysis.unavailable") {
		return false, nil
	}
	return err == nil && len(catalog.Datasets) > 0, err
}

func (a *Adapter) Invoke(ctx context.Context, r sdk.Request) (sdk.Result, error) {
	result, err := a.invoke(ctx, r)
	if code := recoverableCode(err); code != "" {
		return recoverableResult(code), nil
	}
	return result, err
}

func (a *Adapter) invoke(ctx context.Context, r sdk.Request) (sdk.Result, error) {
	in, err := prepare(r)
	if err != nil {
		return sdk.Result{}, err
	}
	source := a.source()
	if source == nil {
		return sdk.Result{}, failure("unavailable", "host_unavailable")
	}
	out := output{Operation: in.Operation, SourceIdentity: source.BusinessSourceIdentity(), RequestSHA256: digest(in)}
	if in.Operation == "catalog" {
		catalog, err := source.AnalysisCatalog(ctx, in.catalogRequest(), r.Authority)
		if err != nil {
			return sdk.Result{}, err
		}
		out.Catalog = &catalog
	} else {
		result, err := source.RunAnalysis(ctx, *in.Spec, r.Authority)
		if err != nil {
			return sdk.Result{}, err
		}
		if result.Source.Proof == "" || !result.Source.Complete || !result.Coverage.Complete || result.Coverage.Truncated || result.Coverage.ReturnedRows != len(result.Rows) || result.Coverage.RequestedMaxRows != result.Spec.MaxRows || !validPresentation(result) || result.Source.DatasetKey != in.Spec.DatasetKey || result.Spec.DatasetKey != in.Spec.DatasetKey || result.Source.DefinitionVersion == "" || result.Source.DataVersion == "" || result.Source.QueriedAt == "" {
			return sdk.Result{}, failure("unavailable", "execution_result_invalid")
		}
		out.Result = &result
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return sdk.Result{}, err
	}
	if len(raw) > Definitions()[0].MaxOutputBytes {
		return sdk.Result{}, failure("bad_request", "result_too_large_narrow_spec")
	}
	if out.Result != nil {
		if err := source.AuthorizeAnalysisResult(ctx, model.AnalysisResultAuthorization{Request: *in.Spec, Result: *out.Result}, r.Authority); err != nil {
			return sdk.Result{}, err
		}
	}
	return sdk.Result{Status: "completed", Content: raw}, nil
}

var positiveAnalysisCount = regexp.MustCompile(`^[1-9][0-9]{0,63}$`)

func validPresentation(result model.AnalysisResult) bool {
	if result.Coverage.Missing == nil || len(result.References) == 0 || len(result.References) > 16 {
		return false
	}
	columns := map[string]model.AnalysisColumn{}
	for _, column := range result.Columns {
		columns[column.Key] = column
	}
	for _, missing := range result.Coverage.Missing {
		if columns[missing.Column].Key == "" || strings.TrimSpace(missing.Code) == "" || !positiveAnalysisCount.MatchString(missing.Count) {
			return false
		}
	}
	chart := result.Visualization.Chart
	if chart == nil {
		return strings.TrimSpace(result.Visualization.OmittedReason) != ""
	}
	if result.Visualization.OmittedReason != "" || (chart.Type != "bar" && chart.Type != "line") || columns[chart.XColumn].Key == "" || len(chart.YColumns) == 0 || len(chart.YColumns) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, key := range chart.YColumns {
		column := columns[key]
		if seen[key] || column.Key == "" || !map[string]bool{"integer": true, "decimal": true, "currency": true, "percent": true, "number": true}[column.Type] {
			return false
		}
		seen[key] = true
	}
	return true
}

func (a *Adapter) AuthorizeResult(ctx context.Context, r sdk.Request, result sdk.Result) error {
	return a.authorizeResult(ctx, r, result, false)
}

func (a *Adapter) AuthorizeResultRead(ctx context.Context, r sdk.Request, result sdk.Result) error {
	return a.authorizeResult(ctx, r, result, true)
}

func (a *Adapter) authorizeResult(ctx context.Context, r sdk.Request, result sdk.Result, independent bool) error {
	in, err := prepare(r)
	if err != nil {
		return err
	}
	// Fixed, data-free diagnostic results carry no owner values or proof. They
	// may be replayed under current tool authorization without rerunning a query.
	if !independent && result.Status == "failed" && recoverableCode(&sdk.Error{Code: result.ErrorCode}) != "" {
		if a.source() != nil && digest(result) == digest(recoverableResult(result.ErrorCode)) {
			return nil
		}
		return failure("forbidden", "result_invalid")
	}
	if result.Status != "completed" || result.Completion != "" || result.ErrorCode != "" || result.ResourceID != "" || len(result.Content) > Definitions()[0].MaxOutputBytes {
		return failure("forbidden", "result_invalid")
	}
	var out output
	if decode(result.Content, &out) != nil {
		return failure("forbidden", "result_invalid")
	}
	source := a.source()
	if source == nil || out.SourceIdentity != source.BusinessSourceIdentity() || out.Operation != in.Operation || out.RequestSHA256 != digest(in) {
		return failure("forbidden", "source_changed")
	}
	if in.Operation == "catalog" {
		if out.Catalog == nil || out.Result != nil {
			return failure("forbidden", "result_invalid")
		}
		if independent {
			if r.ResultProducer != nil {
				reader, ok := source.(SharedResultReadSource)
				if !ok || out.Catalog.ReadProof == "" {
					return &sdk.Error{Class: "unavailable", Code: sdk.ResultReadUnsupportedCode}
				}
				return reader.AuthorizeSharedAnalysisCatalogRead(ctx, model.AnalysisCatalogReadAuthorization{Request: in.catalogRequest(), Result: *out.Catalog}, r.Authority, *r.ResultProducer)
			}
			reader, ok := source.(ResultReadSource)
			if !ok || out.Catalog.ReadProof == "" {
				return &sdk.Error{Class: "unavailable", Code: sdk.ResultReadUnsupportedCode}
			}
			return reader.AuthorizeAnalysisCatalogRead(ctx, model.AnalysisCatalogReadAuthorization{Request: in.catalogRequest(), Result: *out.Catalog}, r.Authority)
		}
		current, err := source.AnalysisCatalog(ctx, in.catalogRequest(), r.Authority)
		if err != nil {
			return err
		}
		// Existing catalogs retain the full execution and metadata checks;
		// a newly issued read attestation alone does not make them stale.
		if out.Catalog.ReadProof == "" {
			current.ReadProof = ""
		}
		if digest(current) != digest(out.Catalog) {
			return failure("forbidden", "catalog_changed")
		}
		return nil
	}
	if out.Result == nil || out.Catalog != nil {
		return failure("forbidden", "result_invalid")
	}
	if independent {
		if r.ResultProducer != nil {
			reader, ok := source.(SharedResultReadSource)
			if !ok || out.Result.Source.ReadProof == "" {
				return &sdk.Error{Class: "unavailable", Code: sdk.ResultReadUnsupportedCode}
			}
			return reader.AuthorizeSharedAnalysisResultRead(ctx, model.AnalysisResultAuthorization{Request: *in.Spec, Result: *out.Result}, r.Authority, *r.ResultProducer)
		}
		reader, ok := source.(ResultReadSource)
		if !ok || out.Result.Source.ReadProof == "" {
			return &sdk.Error{Class: "unavailable", Code: sdk.ResultReadUnsupportedCode}
		}
		return reader.AuthorizeAnalysisResultRead(ctx, model.AnalysisResultAuthorization{Request: *in.Spec, Result: *out.Result}, r.Authority)
	}
	return source.AuthorizeAnalysisResult(ctx, model.AnalysisResultAuthorization{Request: *in.Spec, Result: *out.Result}, r.Authority)
}

// Optional source-owner reading policy; legacy result authorization is not a grant.
type ResultReadSource interface {
	AuthorizeAnalysisResultRead(context.Context, model.AnalysisResultAuthorization, sdk.Authority) error
	AuthorizeAnalysisCatalogRead(context.Context, model.AnalysisCatalogReadAuthorization, sdk.Authority) error
}

type SharedResultReadSource interface {
	AuthorizeSharedAnalysisResultRead(context.Context, model.AnalysisResultAuthorization, sdk.Authority, sdk.Authority) error
	AuthorizeSharedAnalysisCatalogRead(context.Context, model.AnalysisCatalogReadAuthorization, sdk.Authority, sdk.Authority) error
}

func (a *Adapter) ConversationToolResultReadAvailable(_ context.Context, authority sdk.Authority, key string) (bool, error) {
	// Exact source/data authorization occurs against the saved result below.
	// Discovery of executable datasets is not a prerequisite for this read.
	return key == Key && authority.Known && authority.RuntimeID != "" && authority.WorkspaceID != "" && authority.UserID != "" && a.source() != nil, nil
}
