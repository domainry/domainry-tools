// Package reporttools adapts Report-owned use cases to the neutral tool port.
// It has no Agent, Runtime, Record, provider or persistence implementation dependency.
package reporttools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	reportsdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools-sdk/schema"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

// Source is a trusted host port over public Report DTOs and neutral caller
// identity. The host resolves current authority; no token or SQL crosses it.
type Source interface {
	BusinessSourceIdentity() string
	ReportCatalog(context.Context, reportmodel.ReportCatalogRequest, sdk.Authority) (reportmodel.ReportCatalog, error)
	QueryReport(context.Context, reportmodel.ReportObjectSQLRequest, sdk.Authority) (reportmodel.ReportQueryResult, error)
	AuthorizeReportResult(context.Context, reportmodel.ReportQueryResultAuthorization, sdk.Authority) error
}

type Adapter struct {
	// Source is resolved on each use so a deferred host can bind before workers
	// start. Products may select the tool before configuring a business host.
	Source    func() Source
	Authorize tools.Authorizer
}

type input struct {
	Operation  string         `json:"operation"`
	ReportKey  string         `json:"report_key,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
	Cursor     string         `json:"cursor,omitempty"`
	PageSize   int            `json:"page_size,omitempty"`
}

type output struct {
	Operation      string                         `json:"operation"`
	SourceIdentity string                         `json:"source_identity"`
	RequestSHA256  string                         `json:"request_sha256"`
	Catalog        *reportmodel.ReportCatalog     `json:"catalog,omitempty"`
	Result         *reportmodel.ReportQueryResult `json:"result,omitempty"`
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
	if a == nil || a.Authorize == nil || a.Source == nil {
		return fmt.Errorf("report tool host configuration is incomplete")
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
		// Discovery needs a visible catalog entry. A concrete call instead goes
		// through Catalog/QueryReport/AuthorizeReportResult, which authorize its
		// exact request in the owner. Reading the whole catalog here again adds
		// no authority and amplifies every saved-page revalidation.
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
	catalog, err := source.ReportCatalog(ctx, reportmodel.ReportCatalogRequest{Page: reportmodel.ReportPageRequest{PageSize: 1}}, authority)
	if deniedOrUnavailable(err) {
		return false, nil
	}
	return err == nil && len(catalog.Reports) > 0, err
}

func deniedOrUnavailable(err error) bool {
	var tool *sdk.Error
	if errors.As(err, &tool) {
		return tool.Class == "forbidden" || tool.Class == "not_found" || tool.Class == "unavailable"
	}
	var report *reportsdk.Error
	return errors.As(err, &report) && (report.StatusCode == 401 || report.StatusCode == 403 || report.StatusCode == 404 || report.StatusCode == 503)
}

func failure(class, code string) error { return &sdk.Error{Class: class, Code: "report." + code} }

func decode(raw []byte, target any) error {
	if !utf8.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return failure("bad_request", "invalid_data")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return failure("bad_request", "invalid_data")
	}
	return nil
}

func prepare(r sdk.Request) (input, error) {
	var in input
	if r.Definition.Key != Key || r.Call.Name != Key || len(r.Call.Arguments) > 65536 {
		return in, failure("bad_request", "invalid_input")
	}
	compiled, err := schema.CompileSchema(Definitions()[0].InputSchema)
	if err != nil || schema.ValidateJSON(compiled, []byte(r.Call.Arguments)) != nil || decode([]byte(r.Call.Arguments), &in) != nil {
		return in, failure("bad_request", "invalid_input")
	}
	if in.PageSize == 0 {
		in.PageSize = 10
	}
	return in, nil
}

func (in input) catalogRequest() reportmodel.ReportCatalogRequest {
	return reportmodel.ReportCatalogRequest{ReportKey: in.ReportKey, Page: reportmodel.ReportPageRequest{PageSize: in.PageSize, Cursor: in.Cursor}}
}
func (in input) queryRequest() reportmodel.ReportObjectSQLRequest {
	return reportmodel.ReportObjectSQLRequest{ReportKey: in.ReportKey, Parameters: in.Parameters, Page: reportmodel.ReportPageRequest{PageSize: in.PageSize, Cursor: in.Cursor}}
}
func digest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (a *Adapter) Invoke(ctx context.Context, r sdk.Request) (sdk.Result, error) {
	in, err := prepare(r)
	if err != nil {
		return sdk.Result{}, err
	}
	source := a.source()
	if source == nil {
		return sdk.Result{}, failure("unavailable", "host_unavailable")
	}
	out := output{Operation: in.Operation, SourceIdentity: source.BusinessSourceIdentity(), RequestSHA256: digest(in)}
	switch in.Operation {
	case "catalog":
		catalog, err := source.ReportCatalog(ctx, in.catalogRequest(), r.Authority)
		if err != nil {
			return sdk.Result{}, err
		}
		out.Catalog = &catalog
	case "query":
		result, err := source.QueryReport(ctx, in.queryRequest(), r.Authority)
		if err != nil {
			return sdk.Result{}, err
		}
		// Empty/validation-only host responses cannot become a completed call.
		if result.Source.Proof == "" || result.Source.ReportKey != in.ReportKey || result.Summary.Key != in.ReportKey || result.Summary.ExecutionMode != "object_sql_v1" {
			return sdk.Result{}, failure("unavailable", "execution_result_invalid")
		}
		if err := source.AuthorizeReportResult(ctx, reportmodel.ReportQueryResultAuthorization{Query: in.queryRequest(), Result: result}, r.Authority); err != nil {
			return sdk.Result{}, err
		}
		out.Result = &result
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return sdk.Result{}, err
	}
	if len(raw) > Definitions()[0].MaxOutputBytes {
		return sdk.Result{}, failure("bad_request", "page_too_large_reduce_page_size")
	}
	return sdk.Result{Status: "completed", Content: raw}, nil
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
				return reader.AuthorizeSharedReportCatalogRead(ctx, reportmodel.ReportCatalogReadAuthorization{Request: in.catalogRequest(), Result: *out.Catalog}, r.Authority, *r.ResultProducer)
			}
			reader, ok := source.(ResultReadSource)
			if !ok || out.Catalog.ReadProof == "" {
				return &sdk.Error{Class: "unavailable", Code: sdk.ResultReadUnsupportedCode}
			}
			return reader.AuthorizeReportCatalogRead(ctx, reportmodel.ReportCatalogReadAuthorization{Request: in.catalogRequest(), Result: *out.Catalog}, r.Authority)
		}
		current, err := source.ReportCatalog(ctx, in.catalogRequest(), r.Authority)
		if err != nil {
			return err
		}
		// Old saved catalogs predate read attestations. They still require
		// current execution authorization and an exact metadata comparison.
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
			return reader.AuthorizeSharedReportResultRead(ctx, reportmodel.ReportQueryResultAuthorization{Query: in.queryRequest(), Result: *out.Result}, r.Authority, *r.ResultProducer)
		}
		reader, ok := source.(ResultReadSource)
		if !ok || out.Result.Source.ReadProof == "" {
			return &sdk.Error{Class: "unavailable", Code: sdk.ResultReadUnsupportedCode}
		}
		return reader.AuthorizeReportResultRead(ctx, reportmodel.ReportQueryResultAuthorization{Query: in.queryRequest(), Result: *out.Result}, r.Authority)
	}
	return source.AuthorizeReportResult(ctx, reportmodel.ReportQueryResultAuthorization{Query: in.queryRequest(), Result: *out.Result}, r.Authority)
}

// Optional source-owner reading policy; legacy result authorization is not a grant.
type ResultReadSource interface {
	AuthorizeReportResultRead(context.Context, reportmodel.ReportQueryResultAuthorization, sdk.Authority) error
	AuthorizeReportCatalogRead(context.Context, reportmodel.ReportCatalogReadAuthorization, sdk.Authority) error
}

type SharedResultReadSource interface {
	AuthorizeSharedReportResultRead(context.Context, reportmodel.ReportQueryResultAuthorization, sdk.Authority, sdk.Authority) error
	AuthorizeSharedReportCatalogRead(context.Context, reportmodel.ReportCatalogReadAuthorization, sdk.Authority, sdk.Authority) error
}

func (a *Adapter) ConversationToolResultReadAvailable(_ context.Context, authority sdk.Authority, key string) (bool, error) {
	// Exact source/data authorization occurs against the saved result below.
	// Discovery of executable datasets is not a prerequisite for this read.
	return key == Key && authority.Known && authority.RuntimeID != "" && authority.WorkspaceID != "" && authority.UserID != "" && a.source() != nil, nil
}
