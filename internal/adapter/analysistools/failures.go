package analysistools

import (
	"encoding/json"
	"errors"

	report "github.com/domainry/domainry-report-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
)

// Only fixed analysis diagnostics cross into model context. Never expose
// underlying SQL, driver errors, caller-controlled messages or partial rows.
func recoverableCode(err error) string {
	code := ""
	var tool *sdk.Error
	var owner *report.Error
	if errors.As(err, &tool) {
		code = tool.Code
	} else if errors.As(err, &owner) {
		code = owner.Code
	}
	switch code {
	case "backend.report.analysis.result_limit_exceeded", "analysis.result_too_large_narrow_spec", "backend.report.analysis.spec_invalid", "backend.report.analysis.arithmetic_limit", "backend.report.analysis.source_changed":
		return code
	}
	return ""
}

func recoverableResult(code string) sdk.Result {
	recovery := "Narrow the filters or grouping and run a complete analysis again. Never substitute sampled query pages."
	switch code {
	case "backend.report.analysis.spec_invalid":
		recovery = "Read the current dataset catalog and correct the structured spec. Use only declared columns and supported operations."
	case "backend.report.analysis.arithmetic_limit":
		recovery = "Simplify the calculation or narrow the input range; no result was returned."
	case "backend.report.analysis.source_changed":
		recovery = "The source changed during analysis. Read current metadata and explicitly run a fresh analysis."
	}
	raw, _ := json.Marshal(map[string]string{"error": code, "recovery": recovery})
	return sdk.Result{Status: "failed", ErrorCode: code, Content: raw}
}
