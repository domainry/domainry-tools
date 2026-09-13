package analysistools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	model "github.com/domainry/domainry-report-sdk/model"
	sdk "github.com/domainry/domainry-tools-sdk"
)

// An upgraded owner can attest new results while old stored bodies still lack
// ReadProof. Its explicit denial must never be confused with that legacy case.
type upgradedReadSource struct {
	*sourceFixture
	readChecks int
	denial     error
}

func (s *upgradedReadSource) AnalysisCatalog(ctx context.Context, in model.AnalysisCatalogRequest, a sdk.Authority) (model.AnalysisCatalog, error) {
	out, err := s.sourceFixture.AnalysisCatalog(ctx, in, a)
	out.ReadProof = "new-owner-read-attestation"
	return out, err
}
func (s *upgradedReadSource) AuthorizeAnalysisResultRead(context.Context, model.AnalysisResultAuthorization, sdk.Authority) error {
	s.readChecks++
	return s.denial
}
func (s *upgradedReadSource) AuthorizeAnalysisCatalogRead(context.Context, model.AnalysisCatalogReadAuthorization, sdk.Authority) error {
	s.readChecks++
	return s.denial
}

func TestAnalysisLegacyResultsRetainExecutionChecksAfterOwnerUpgrade(t *testing.T) {
	for _, arguments := range []string{`{"operation":"run","spec":{"dataset_key":"sales","measures":[{"key":"total","function":"sum","field":"amount"}]}}`, `{"operation":"catalog"}`} {
		t.Run(arguments, func(t *testing.T) {
			adapter, selected, original, request, grant := fixture(t)
			request.Call.Arguments = arguments
			saved, err := selected.InvokeConversationTool(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			current := &upgradedReadSource{sourceFixture: original, denial: &sdk.Error{Class: "forbidden", Code: "source.read_denied"}}
			adapter.Source = func() Source { return current }
			var coded *sdk.Error
			if err := selected.AuthorizeConversationToolResultRead(t.Context(), request, saved); !errors.As(err, &coded) || coded.Code != sdk.ResultReadUnsupportedCode || current.readChecks != 0 {
				t.Fatalf("legacy body acquired independent read permission: %v", err)
			}
			if err := selected.AuthorizeConversationToolResult(t.Context(), request, saved); err != nil {
				t.Fatalf("owner upgrade broke current authorized legacy read: %v", err)
			}
			*grant = false
			if err := selected.AuthorizeConversationToolResult(t.Context(), request, saved); err == nil {
				t.Fatal("legacy fallback bypassed execution permission")
			}
			*grant = true
			var envelope output
			if err := json.Unmarshal(saved.Content, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Catalog != nil {
				envelope.Catalog.ReadProof = "invalid-attestation"
			} else {
				envelope.Result.Source.ReadProof = "invalid-attestation"
			}
			saved.Content, _ = json.Marshal(envelope)
			if err := selected.AuthorizeConversationToolResultRead(t.Context(), request, saved); !errors.Is(err, current.denial) || current.readChecks != 1 {
				t.Fatalf("explicit source denial downgraded to legacy handling: %v", err)
			}
		})
	}
}
