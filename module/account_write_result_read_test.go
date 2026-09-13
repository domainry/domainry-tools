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
