package module_test

import (
	"context"
	"encoding/json"
	"fmt"
	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
)

type accountReadFixture struct {
	accounts                             []integration.ConnectionAccount
	listAccess, readAccess               integration.ConnectionAccountAccess
	toolAllowed, mismatchSubject, replay bool
	payload                              json.RawMessage
	afterRead                            func()
	mutateSource                         func(*integration.ConnectionAccountReadSource)
	blocked                              map[string]bool
	requests                             []integration.ConnectionAccountReadRequest
	readSubjects                         []integration.ConnectionAccountSubject
}

func newAccountReadFixture() *accountReadFixture {
	f := &accountReadFixture{toolAllowed: true, listAccess: integration.ConnectionAccountAccess{Personal: true, Workspace: true}, readAccess: integration.ConnectionAccountAccess{Personal: true, Workspace: true}, blocked: map[string]bool{}}
	f.accounts = []integration.ConnectionAccount{{Key: "account", WorkspaceID: "workspace", ConnectorKey: "account-provider", ProviderKey: "account-provider", Name: "账号", Scope: integration.ConnectionAccountScopePersonal, OwnerUserID: "alice", Status: "active", UpdatedAt: "revision-1", Readiness: &integration.ConnectionAccountReadiness{Available: true}}}
	return f
}
func (f *accountReadFixture) set(v any) { f.payload, _ = json.Marshal(v) }
func (f *accountReadFixture) ListConnectionAccounts(context.Context, integration.ConnectionAccountSubject) ([]integration.ConnectionAccount, error) {
	return f.accounts, nil
}
func (f *accountReadFixture) subject(_ context.Context, a sdk.Authority, action string) (integration.ConnectionAccountSubject, error) {
	access := f.readAccess
	if action == integration.ActionIntegrationConnectionAccountsList {
		access = f.listAccess
	} else if action != integration.ActionIntegrationConnectionAccountsRead {
		return integration.ConnectionAccountSubject{}, fmt.Errorf("unexpected action %s", action)
	}
	s := integration.ConnectionAccountSubject{WorkspaceID: a.WorkspaceID, UserID: a.UserID, Access: access}
	if f.mismatchSubject {
		s.UserID = "other"
	}
	return s, nil
}
func (f *accountReadFixture) authorize(context.Context, sdk.Request) (sdk.Authorization, error) {
	return sdk.Authorization{Granted: f.toolAllowed}, nil
}
func (f *accountReadFixture) AuthorizeConnectionAccountRead(_ context.Context, s integration.ConnectionAccountSubject, key string, op integration.ConnectionAccountReadOperation) (integration.ConnectionAccountReadAccess, error) {
	if f.blocked[key+":"+op.Operation] {
		return integration.ConnectionAccountReadAccess{}, fmt.Errorf("denied")
	}
	for _, a := range f.accounts {
		if a.Key != key || a.WorkspaceID != s.WorkspaceID || a.Status != "active" {
			continue
		}
		if a.Scope == integration.ConnectionAccountScopePersonal && (!s.Access.Personal || a.OwnerUserID != s.UserID) || a.Scope == integration.ConnectionAccountScopeWorkspace && !s.Access.Workspace {
			continue
		}
		source := integration.ConnectionAccountReadSource{WorkspaceID: s.WorkspaceID, ConnectionKey: key, ConnectorKey: a.ConnectorKey, ProviderKey: a.ProviderKey, AccountUpdatedAt: a.UpdatedAt, Operation: op.Operation, ContractSHA256: op.ContractSHA256}
		return integration.ConnectionAccountReadAccess{Source: source}, nil
	}
	return integration.ConnectionAccountReadAccess{}, fmt.Errorf("denied")
}
func (f *accountReadFixture) ReadConnectionAccount(ctx context.Context, s integration.ConnectionAccountSubject, key string, r integration.ConnectionAccountReadRequest) (integration.ConnectionAccountReadResult, error) {
	access, err := f.AuthorizeConnectionAccountRead(ctx, s, key, r.OperationContract())
	if err != nil {
		return integration.ConnectionAccountReadResult{}, err
	}
	f.requests = append(f.requests, r)
	f.readSubjects = append(f.readSubjects, s)
	if f.mutateSource != nil {
		f.mutateSource(&access.Source)
	}
	out := integration.ConnectionAccountReadResult{Source: access.Source, InvocationID: "invocation", ReadAt: "2026-09-11T10:00:00Z", PayloadAvailable: !f.replay, Payload: append(json.RawMessage(nil), f.payload...)}
	if f.afterRead != nil {
		f.afterRead()
	}
	return out, nil
}
