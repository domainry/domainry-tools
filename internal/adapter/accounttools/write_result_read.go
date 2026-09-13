package accounttools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
)

// Reading a released receipt uses current account data access and the exact
// owner ledger entry. It never requests confirmation, claims an operation or
// calls Invoke/Reconcile; absence/uncertainty is a denial, never a retry.
func (a *WriteAdapter) AuthorizeResultRead(ctx context.Context, r sdk.Request, out sdk.Result) error {
	if a.Accounts == nil || a.Writes == nil || a.Subject == nil || out.Status != "completed" || out.ErrorCode != "" || len(out.Content) > r.Definition.MaxOutputBytes {
		return a.failure("write_source_invalid")
	}
	var envelope writeEnvelope
	if decode(out.Content, &envelope) != nil {
		return a.failure("write_source_invalid")
	}
	if r.Definition.Key == a.Family.AccountsKey {
		if out.Completion != "" || out.ResourceID != "" || envelope.RecordedAt != "" {
			return a.failure("write_source_invalid")
		}
		return a.authorizeAccountsForAction(ctx, r, envelope, integration.ActionIntegrationConnectionAccountsRead)
	}
	in, err := a.prepare(r)
	if err != nil || in.Present == nil || out.Completion != in.Completion || out.ResourceID == "" || len(out.ResourceID) > 2048 || len(envelope.Sources) != 1 || strings.TrimSpace(r.IdempotencyKey) == "" || len(r.IdempotencyKey) > 4096 || r.ConversationID == "" || r.RunID == "" || r.Call.ID == "" {
		return a.failure("write_source_invalid")
	}
	if _, err = time.Parse(time.RFC3339Nano, envelope.RecordedAt); err != nil {
		return a.failure("write_source_invalid")
	}
	subject, err := a.subject(ctx, r.Authority, integration.ActionIntegrationConnectionAccountsRead)
	if err != nil {
		return err
	}
	source := envelope.Sources[0]
	op := integration.ConnectionAccountWriteOperation{Operation: r.Definition.Key, ContractSHA256: a.Family.OperationSHA256(r.Definition.Key)}
	if op.Validate() != nil || !writeSourceMatches(source, subject, in.AccountKey, op) || source.AccountUpdatedAt != in.AccountUpdatedAt {
		return a.failure("write_source_changed")
	}
	identity, _ := json.Marshal([]string{r.Authority.RuntimeID, r.IdempotencyKey})
	hash := sha256.Sum256(identity)
	payload, err := json.Marshal(in.Payload)
	if err != nil {
		return a.failure("write_source_invalid")
	}
	request := integration.ConnectionAccountWriteRequest{RequestID: "account-tool:" + hex.EncodeToString(hash[:]), ExpectedSource: source, Payload: payload}
	original, err := a.Writes.ReadConnectionAccountWriteReceipt(ctx, subject, in.AccountKey, request)
	if err != nil || original.Status != integration.AccountWriteSucceeded || original.Source != source || original.InvocationID != out.ResourceID || original.RecordedAt != envelope.RecordedAt || len(original.Receipt) > 32<<10 {
		return a.failure("write_source_changed")
	}
	data, err := in.Present(original.Receipt, original.InvocationID)
	if err != nil {
		return a.failure("write_source_invalid")
	}
	expected, err := json.Marshal(data)
	if err != nil || !sameReceiptJSON(expected, envelope.Data) {
		return a.failure("write_source_changed")
	}
	return ctx.Err()
}

func sameReceiptJSON(expected, actual []byte) bool {
	canonical := func(raw []byte) ([]byte, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
	// Envelope decoding already guarantees one JSON value. Preserve decimal
	// strings/numbers exactly while ignoring insignificant object key order.
	left, err := canonical(expected)
	if err != nil {
		return false
	}
	right, err := canonical(actual)
	return err == nil && bytes.Equal(left, right)
}

func (a *WriteAdapter) ConversationToolResultReadAvailable(ctx context.Context, authority sdk.Authority, key string) (bool, error) {
	if a.Accounts == nil || a.Writes == nil || a.Subject == nil || key != a.Family.AccountsKey && a.Family.OperationSHA256(key) == "" {
		return false, nil
	}
	if _, err := a.subject(ctx, authority, integration.ActionIntegrationConnectionAccountsRead); err != nil {
		return false, availabilityError(err)
	}
	return ctx.Err() == nil, ctx.Err()
}
