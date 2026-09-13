package module_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/domainry/domainry-connector-sdk/calendarwrite"
	"github.com/domainry/domainry-connector-sdk/mailwrite"
	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	module "github.com/domainry/domainry-tools/module"
)

// This fixture is the public Integration port. Provider/ledger behavior is
// tested by that owner; these tests observe exactly what Tools sends to it.
type accountWriteFixture struct {
	*accountReadFixture
	writeAccess                 integration.ConnectionAccountAccess
	approved, extraConfirmation bool
	strictReceipts              bool
	verifyCalls                 int
	writes, lookups             []integration.ConnectionAccountWriteRequest
	writeSubjects               []integration.ConnectionAccountSubject
	status                      string
	modifyResult                func(*integration.ConnectionAccountWriteResult)
	afterWrite                  func()
}

func newAccountWriteFixture() *accountWriteFixture {
	return &accountWriteFixture{accountReadFixture: newAccountReadFixture(), writeAccess: integration.ConnectionAccountAccess{Personal: true, Workspace: true}, approved: true, status: integration.AccountWriteSucceeded}
}
func (f *accountWriteFixture) subject(ctx context.Context, a sdk.Authority, action string) (integration.ConnectionAccountSubject, error) {
	if action != integration.ActionIntegrationConnectionAccountsWrite {
		return f.accountReadFixture.subject(ctx, a, action)
	}
	s := integration.ConnectionAccountSubject{WorkspaceID: a.WorkspaceID, UserID: a.UserID, Access: f.writeAccess}
	if f.mismatchSubject {
		s.UserID = "other"
	}
	return s, nil
}
func (f *accountWriteFixture) authorize(context.Context, sdk.Request) (sdk.Authorization, error) {
	return sdk.Authorization{Granted: f.toolAllowed, ConfirmationRequired: f.extraConfirmation}, nil
}
func (f *accountWriteFixture) VerifyConversationToolConfirmation(context.Context, sdk.Request) (bool, error) {
	f.verifyCalls++
	return f.approved, nil
}
func (f *accountWriteFixture) AuthorizeConnectionAccountWrite(ctx context.Context, s integration.ConnectionAccountSubject, key string, op integration.ConnectionAccountWriteOperation) (integration.ConnectionAccountWriteAccess, error) {
	if err := op.Validate(); err != nil {
		return integration.ConnectionAccountWriteAccess{}, err
	}
	read, err := f.AuthorizeConnectionAccountRead(ctx, s, key, integration.ConnectionAccountReadOperation{Operation: op.Operation, ContractSHA256: op.ContractSHA256})
	r := read.Source
	return integration.ConnectionAccountWriteAccess{Source: integration.ConnectionAccountWriteSource{WorkspaceID: r.WorkspaceID, ConnectionKey: r.ConnectionKey, ConnectorKey: r.ConnectorKey, ProviderKey: r.ProviderKey, AccountUpdatedAt: r.AccountUpdatedAt, Operation: r.Operation, ContractSHA256: r.ContractSHA256}}, err
}
func (f *accountWriteFixture) result(ctx context.Context, s integration.ConnectionAccountSubject, key string, r integration.ConnectionAccountWriteRequest) (integration.ConnectionAccountWriteResult, error) {
	if err := r.Validate(); err != nil {
		return integration.ConnectionAccountWriteResult{}, err
	}
	a, err := f.AuthorizeConnectionAccountWrite(ctx, s, key, r.ExpectedSource.OperationContract())
	if err != nil || a.Source != r.ExpectedSource {
		return integration.ConnectionAccountWriteResult{}, fmt.Errorf("source changed")
	}
	out := integration.ConnectionAccountWriteResult{Source: a.Source, Status: f.status, InvocationID: "owner-invocation", RecordedAt: "2026-09-12T10:00:00Z"}
	if f.status == integration.AccountWriteSucceeded {
		switch r.ExpectedSource.Operation {
		case calendarwrite.CreateOperationKey, calendarwrite.UpdateOperationKey:
			var q struct {
				CalendarID string `json:"calendar_id"`
				EventID    string `json:"event_id"`
			}
			_ = json.Unmarshal(r.Payload, &q)
			outcome := "updated"
			if q.EventID == "" {
				q.EventID = "new-event"
				outcome = "created"
			}
			out.Receipt, _ = json.Marshal(calendarwrite.Result{RequestRef: out.InvocationID, Outcome: outcome, CalendarID: q.CalendarID, EventID: q.EventID, Version: `"etag-2"`, Notifications: "requested"})
		case mailwrite.SendOperationKey, mailwrite.ReplyOperationKey:
			var q struct {
				MessageID string `json:"message_id"`
			}
			_ = json.Unmarshal(r.Payload, &q)
			out.Receipt, _ = json.Marshal(mailwrite.Result{RequestRef: out.InvocationID, Status: "accepted", InReplyToMessageID: q.MessageID, AcceptedAt: out.RecordedAt, Delivery: "unknown"})
		}
	}
	if f.modifyResult != nil {
		f.modifyResult(&out)
	}
	return out, nil
}
func (f *accountWriteFixture) WriteConnectionAccount(ctx context.Context, s integration.ConnectionAccountSubject, key string, r integration.ConnectionAccountWriteRequest) (integration.ConnectionAccountWriteResult, error) {
	f.writes = append(f.writes, r)
	f.writeSubjects = append(f.writeSubjects, s)
	out, err := f.result(ctx, s, key, r)
	if f.afterWrite != nil {
		f.afterWrite()
	}
	return out, err
}
func (f *accountWriteFixture) ReadConnectionAccountWriteReceipt(ctx context.Context, s integration.ConnectionAccountSubject, key string, r integration.ConnectionAccountWriteRequest) (integration.ConnectionAccountWriteResult, error) {
	f.lookups = append(f.lookups, r)
	if f.strictReceipts {
		found := false
		for i, original := range f.writes {
			found = found || reflect.DeepEqual(original, r) && f.writeSubjects[i].UserID == s.UserID && f.writeSubjects[i].WorkspaceID == s.WorkspaceID
		}
		if !found {
			return integration.ConnectionAccountWriteResult{Source: r.ExpectedSource, Status: integration.AccountWriteNotFound}, nil
		}
	}
	return f.result(ctx, s, key, r)
}

func writeDefinitions() []sdk.Definition {
	return append(module.CalendarWriteDefinitions(), module.MailWriteDefinitions()...)
}
func (f *accountWriteFixture) adapters() (*module.CalendarWriteAdapter, *module.MailWriteAdapter) {
	return &module.CalendarWriteAdapter{Accounts: f, Reads: f, Writes: f, Subject: f.subject, Authorize: f.authorize, Confirmation: f}, &module.MailWriteAdapter{Accounts: f, Writes: f, Subject: f.subject, Authorize: f.authorize, Confirmation: f}
}
func (f *accountWriteFixture) selection(t *testing.T) *module.Selection {
	t.Helper()
	reg := module.NewRegistry()
	c, m := f.adapters()
	if err := c.Register(reg); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(reg); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, d := range writeDefinitions() {
		keys = append(keys, d.Key)
	}
	s, err := reg.Select(keys)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func writeRequest(t *testing.T, key, payload string) sdk.Request {
	t.Helper()
	r := sdk.Request{Authority: sdk.Authority{Known: true, RuntimeID: "runtime", WorkspaceID: "workspace", UserID: "alice"}, ConversationID: "conversation", RunID: "run", Step: 1, Call: sdk.Call{ID: "call", Name: key, Arguments: `{"account_key":"account","account_updated_at":"revision-1","request":` + payload + `}`}, IdempotencyKey: "host-stable-id", ConfirmationID: "approval", Confirmation: &sdk.Confirmation{ID: "approval"}}
	for _, d := range writeDefinitions() {
		if d.Key == key {
			r.Definition = d
			return r
		}
	}
	t.Fatalf("unknown tool %s", key)
	return r
}

const calendarCreatePayload = `{"calendar_id":"primary","event":{"title":"明确日程","start":{"date_time":"2026-11-01T01:30:00-04:00","time_zone":"America/New_York"},"end":{"date_time":"2026-11-01T01:30:00-05:00","time_zone":"America/New_York"},"attendees":[{"address":"one@example.test","kind":"required"}]},"notifications":"notify_attendees"}`
const calendarUpdatePayload = `{"calendar_id":"primary","event_id":"exact-event","expected_version":"\"etag-1\"","scope":"event","changes":{"description":"","location":"","attendees":[]},"notifications":"notify_attendees"}`
const outgoingMessage = `{"to":[{"address":"to@example.test"}],"cc":[{"address":"cc@example.test"}],"bcc":[{"address":"bcc@example.test"}],"subject":"明确主题","text":"完整正文\n第二行"}`

func writePayload(key string) string {
	switch key {
	case calendarwrite.CreateOperationKey:
		return calendarCreatePayload
	case calendarwrite.UpdateOperationKey:
		return calendarUpdatePayload
	case mailwrite.SendOperationKey:
		return `{"message":` + outgoingMessage + `}`
	case mailwrite.ReplyOperationKey:
		return `{"message_id":"original-message","message":` + outgoingMessage + `}`
	}
	panic(key)
}
