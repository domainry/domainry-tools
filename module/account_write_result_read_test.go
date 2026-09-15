package module_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/domainry/domainry-connector-sdk/calendarwrite"
	"github.com/domainry/domainry-connector-sdk/mailwrite"
	integration "github.com/domainry/domainry-integration-sdk"
)

func TestSharedAccountWriteResultReadUsesOriginalActorAfterCheckingCurrentReader(t *testing.T) {
	for _, key := range []string{calendarwrite.CreateOperationKey, calendarwrite.UpdateOperationKey, mailwrite.SendOperationKey, mailwrite.ReplyOperationKey} {
		t.Run(key, func(t *testing.T) {
			f := newAccountWriteFixture()
			f.strictReceipts = true
			f.accounts[0].Scope, f.accounts[0].OwnerUserID = integration.ConnectionAccountScopeWorkspace, ""
			selected := f.selection(t)
			request := writeRequest(t, key, writePayload(key))
			request.Authority.RoleKey = "professional"
			saved, err := selected.InvokeConversationTool(t.Context(), request)
			if err != nil || len(f.writes) != 1 {
				t.Fatal("original actor did not execute", err)
			}
			producer := request.Authority
			request.Authority.UserID, request.Authority.RoleKey = "bob", "reader"
			request.ResultProducer = &producer
			request.Confirmation, request.ConfirmationID = nil, ""
			approvals := f.verifyCalls
			f.writeAccess, f.toolAllowed, f.approved = integration.ConnectionAccountAccess{}, false, false
			if err := selected.AuthorizeConversationToolResultRead(t.Context(), request, saved); err != nil {
				t.Fatal("authorized workspace reader could not verify the original actor's receipt", err)
			}
			if len(f.writes) != 1 || f.verifyCalls != approvals || len(f.lookups) != 1 || !reflect.DeepEqual(f.lookups[0], f.writes[0]) {
				t.Fatal("shared reading changed the original request or repeated an effect/approval")
			}
			for _, scenario := range []string{"reader_scope", "personal_account", "unknown_producer", "wrong_runtime", "wrong_workspace", "wrong_actor", "withdraw_during_lookup"} {
				t.Run(scenario, func(t *testing.T) {
					priorAccess, priorAccount := f.readAccess, f.accounts[0]
					changed := request
					changedProducer := producer
					changed.ResultProducer = &changedProducer
					defer func() { f.readAccess, f.accounts[0], f.afterLookup = priorAccess, priorAccount, nil }()
					switch scenario {
					case "reader_scope":
						f.readAccess.Workspace = false
					case "personal_account":
						f.accounts[0].Scope, f.accounts[0].OwnerUserID = integration.ConnectionAccountScopePersonal, producer.UserID
					case "unknown_producer":
						changedProducer.Known = false
					case "wrong_runtime":
						changedProducer.RuntimeID = "other"
					case "wrong_workspace":
						changedProducer.WorkspaceID = "other"
					case "wrong_actor":
						changedProducer.UserID = "mallory"
					case "withdraw_during_lookup":
						f.afterLookup = func() { f.readAccess.Workspace = false }
					}
					if err := selected.AuthorizeConversationToolResultRead(t.Context(), changed, saved); err == nil {
						t.Fatal("shared receipt accepted an unauthorized reader or incorrect original actor")
					}
				})
			}
		})
	}
}

func TestAccountWriteResultReadUsesOriginalLedgerWithoutExecutionOrApproval(t *testing.T) {
	for _, key := range []string{calendarwrite.CreateOperationKey, calendarwrite.UpdateOperationKey, mailwrite.SendOperationKey, mailwrite.ReplyOperationKey} {
		t.Run(key, func(t *testing.T) {
			f := newAccountWriteFixture()
			f.strictReceipts = true
			selected := f.selection(t)
			request := writeRequest(t, key, writePayload(key))
			saved, err := selected.InvokeConversationTool(t.Context(), request)
			if err != nil || len(f.writes) != 1 {
				t.Fatal(saved, err)
			}
			original := f.writes[0]
			approvals := f.verifyCalls
			request.Confirmation = nil
			request.ConfirmationID = ""
			request.LeaseOwner = ""
			request.Fence = 0
			f.writeAccess = integration.ConnectionAccountAccess{}
			f.toolAllowed = false
			f.approved = false
			if err := selected.AuthorizeConversationToolResultRead(t.Context(), request, saved); err != nil {
				t.Fatal("read coupled to execution or confirmation", err)
			}
			if len(f.lookups) != 1 || !reflect.DeepEqual(f.lookups[0], original) || len(f.writes) != 1 || f.verifyCalls != approvals {
				t.Fatal("read changed identity, resent or checked confirmation")
			}
			if err := selected.AuthorizeConversationToolResult(t.Context(), request, saved); err == nil {
				t.Fatal("independent reading granted raw replay")
			}
			if _, err := selected.InvokeConversationTool(t.Context(), request); err == nil {
				t.Fatal("independent reading granted execution")
			}
			if _, err := selected.ReconcileConversationTool(t.Context(), request); err == nil {
				t.Fatal("independent reading granted recovery")
			}
			f.readAccess = integration.ConnectionAccountAccess{}
			if err := selected.AuthorizeConversationToolResultRead(t.Context(), request, saved); err == nil {
				t.Fatal("revoked account data exposed receipt")
			}
			if len(f.writes) != 1 || f.verifyCalls != approvals {
				t.Fatal("revocation performed operation")
			}
		})
	}
}

func TestAccountWriteResultReadRejectsChangedOriginalAndUnknownReceipt(t *testing.T) {
	for _, scenario := range []string{"id", "payload", "actor", "workspace", "resource", "timestamp", "body", "account", "operation", "unknown", "missing", "failed", "receipt", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAccountWriteFixture()
			f.strictReceipts = true
			selected := f.selection(t)
			request := writeRequest(t, mailwrite.SendOperationKey, writePayload(mailwrite.SendOperationKey))
			saved, err := selected.InvokeConversationTool(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch scenario {
			case "id":
				request.IdempotencyKey = "other"
			case "payload":
				request.Call.Arguments = strings.Replace(request.Call.Arguments, "完整正文", "changed", 1)
			case "actor":
				request.Authority.UserID = "other"
			case "workspace":
				request.Authority.WorkspaceID = "other"
			case "resource":
				saved.ResourceID = "other"
			case "timestamp":
				saved.Content = json.RawMessage(strings.Replace(string(saved.Content), "2026-09-12T10:00:00Z", "2026-09-13T10:00:00Z", 1))
			case "body":
				saved.Content = json.RawMessage(strings.Replace(string(saved.Content), `"delivery":"unknown"`, `"delivery":"delivered"`, 1))
			case "account":
				f.accounts[0].UpdatedAt = "reauthorized"
			case "operation":
				request.Definition.Version = "2"
			case "unknown":
				f.status = integration.AccountWriteUncertain
			case "missing":
				f.status = integration.AccountWriteNotFound
			case "failed":
				f.status = integration.AccountWriteFailed
			case "receipt":
				f.modifyResult = func(out *integration.ConnectionAccountWriteResult) { out.RecordedAt = "2026-09-13T00:00:00Z" }
			case "cancelled":
				var cancel func()
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := selected.AuthorizeConversationToolResultRead(ctx, request, saved); err == nil {
				t.Fatal("changed source accepted")
			}
			if len(f.writes) != 1 {
				t.Fatal("invalid source resent operation")
			}
		})
	}
}
