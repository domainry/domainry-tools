package analysistools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"unicode/utf8"

	model "github.com/domainry/domainry-report-sdk/model"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools-sdk/schema"
)

type input struct {
	Operation  string                 `json:"operation"`
	DatasetKey string                 `json:"dataset_key,omitempty"`
	Cursor     string                 `json:"cursor,omitempty"`
	PageSize   int                    `json:"page_size,omitempty"`
	Spec       *model.AnalysisRequest `json:"spec,omitempty"`
}
type output struct {
	Operation      string                 `json:"operation"`
	SourceIdentity string                 `json:"source_identity"`
	RequestSHA256  string                 `json:"request_sha256"`
	Catalog        *model.AnalysisCatalog `json:"catalog,omitempty"`
	Result         *model.AnalysisResult  `json:"result,omitempty"`
}

func failure(class, code string) error { return &sdk.Error{Class: class, Code: "analysis." + code} }
func digest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
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
	if in.Operation == "catalog" && in.PageSize == 0 {
		in.PageSize = 10
	}
	return in, nil
}
func (in input) catalogRequest() model.AnalysisCatalogRequest {
	return model.AnalysisCatalogRequest{DatasetKey: in.DatasetKey, Page: model.ReportPageRequest{PageSize: in.PageSize, Cursor: in.Cursor}}
}
