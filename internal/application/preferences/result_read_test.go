package preferences

import (
	"context"
	"testing"

	sdk "github.com/domainry/domainry-tools-sdk"
)

type resultReadConnections struct {
	reads   int
	enabled bool
}

func (*resultReadConnections) ToolConnectionAvailable(context.Context, sdk.Authority, string) (bool, error) {
	return false, nil
}
func (p *resultReadConnections) ToolResultReadConnectionAvailable(_ context.Context, _ sdk.Authority, key string) (bool, error) {
	p.reads++
	return p.enabled && key == "report_query", nil
}

func TestResultReadAvailabilityPreservesPreferenceWithoutExecutionCatalog(t *testing.T) {
	a := sdk.Authority{Known: true, RuntimeID: "runtime", WorkspaceID: "workspace", UserID: "user"}
	repo := &fixtureRepository{values: map[[4]string]sdk.Preference{}}
	catalog := &fixtureCatalog{}
	connections := &resultReadConnections{enabled: true}
	s, err := New(repo, catalog, connections)
	if err != nil {
		t.Fatal(err)
	}
	if ready, err := s.ConversationToolResultReadAvailable(t.Context(), a, "report_query"); err != nil || !ready || catalog.calls != 0 {
		t.Fatal("execution catalogue blocked reading", ready, err)
	}
	if ready, _ := s.ConversationToolAvailable(t.Context(), a, "report_query"); ready {
		t.Fatal("read readiness granted execution readiness")
	}
	repo.values[preferenceKey(a, "report_query")] = sdk.Preference{Key: "report_query", Enabled: false, Revision: 1}
	before := connections.reads
	if ready, err := s.ConversationToolResultReadAvailable(t.Context(), a, "report_query"); err != nil || ready || connections.reads != before {
		t.Fatal("disabled preference ignored")
	}
	repo.values[preferenceKey(a, "report_query")] = sdk.Preference{Key: "report_query", Enabled: true, Revision: 2}
	connections.enabled = false
	if ready, err := s.ConversationToolResultReadAvailable(t.Context(), a, "report_query"); err != nil || ready {
		t.Fatal("missing connection ignored")
	}
	if ready, err := s.ConversationToolResultReadAvailable(t.Context(), sdk.Authority{}, "report_query"); err == nil || ready {
		t.Fatal("unknown reader accepted")
	}
}
