package accounttools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools-sdk/schema"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

type WritePorts struct {
	Accounts     Accounts
	Writes       integration.ConnectionAccountWrites
	Subject      SubjectResolver
	Authorize    tools.Authorizer
	Confirmation sdk.ConfirmationVerifier
}

// Each business family owns only its declared payload and receipt presentation.
// Shared account mechanics have no calendar/mail protocol dependency.
type WriteFamily struct {
	Name, AccountsKey, DefaultOperation string
	Definitions                         []sdk.Definition
	Operations                          []string
	OperationSHA256                     func(string) string
	Prepare                             func(string, []byte) (PreparedWrite, error)
}

type PreparedWrite struct {
	AccountKey, AccountUpdatedAt string
	Completion                   string
	Payload                      any
	Present                      func([]byte, string) (any, error)
}

type WriteAdapter struct {
	WritePorts
	Family WriteFamily
}

func (a *WriteAdapter) Register(reg *tools.Registry) error {
	if reg == nil || a.Authorize == nil || a.Confirmation == nil || a.Subject == nil || (a.Accounts == nil) != (a.Writes == nil) || a.Family.Name == "" || a.Family.AccountsKey == "" || a.Family.OperationSHA256 == nil || a.Family.Prepare == nil {
		return fmt.Errorf("account write adapter is incomplete")
	}
	for _, d := range a.Family.Definitions {
		registration := tools.Registration{Definition: d, Authorize: a.AuthorizeTool, Invoke: a.Invoke, Reconcile: a.Reconcile, AuthorizeResult: a.AuthorizeResult, AuthorizeResultRead: a.AuthorizeResultRead}
		if d.Effect == "write" {
			registration.InspectOutcome = a.Reconcile
		}
		if err := reg.Register(registration); err != nil {
			return err
		}
	}
	return nil
}

func (a *WriteAdapter) failure(code string) error {
	return &sdk.Error{Class: "forbidden", Code: a.Family.Name + "." + code}
}
func (a *WriteAdapter) subject(ctx context.Context, authority sdk.Authority, action string) (integration.ConnectionAccountSubject, error) {
	if !authority.Known || authority.RuntimeID == "" || authority.WorkspaceID == "" || authority.UserID == "" {
		return integration.ConnectionAccountSubject{}, a.failure("account_access_denied")
	}
	s, err := a.Subject(ctx, authority, action)
	if err != nil {
		return s, err
	}
	if s.WorkspaceID != authority.WorkspaceID || s.UserID != authority.UserID || !s.Access.Personal && !s.Access.Workspace {
		return integration.ConnectionAccountSubject{}, a.failure("account_access_denied")
	}
	return s, nil
}

func writeSourceMatches(source integration.ConnectionAccountWriteSource, s integration.ConnectionAccountSubject, key string, op integration.ConnectionAccountWriteOperation) bool {
	return source.WorkspaceID == s.WorkspaceID && source.ConnectionKey == key && source.ConnectorKey != "" && source.ProviderKey != "" && source.AccountUpdatedAt != "" && source.Operation == op.Operation && source.ContractSHA256 == op.ContractSHA256
}

func (a *WriteAdapter) access(ctx context.Context, r sdk.Request, in PreparedWrite) (integration.ConnectionAccountSubject, integration.ConnectionAccountWriteSource, error) {
	s, err := a.subject(ctx, r.Authority, integration.ActionIntegrationConnectionAccountsWrite)
	if err != nil {
		return s, integration.ConnectionAccountWriteSource{}, err
	}
	op := integration.ConnectionAccountWriteOperation{Operation: r.Definition.Key, ContractSHA256: a.Family.OperationSHA256(r.Definition.Key)}
	if a.Writes == nil || op.ContractSHA256 == "" {
		return s, integration.ConnectionAccountWriteSource{}, a.failure("write_unavailable")
	}
	access, err := a.Writes.AuthorizeConnectionAccountWrite(ctx, s, in.AccountKey, op)
	if err != nil || !writeSourceMatches(access.Source, s, in.AccountKey, op) || access.Source.AccountUpdatedAt != in.AccountUpdatedAt {
		return s, integration.ConnectionAccountWriteSource{}, a.failure("write_account_changed")
	}
	return s, access.Source, nil
}

func (a *WriteAdapter) AuthorizeTool(ctx context.Context, r sdk.Request) (sdk.Authorization, error) {
	auth, err := a.Authorize(ctx, r)
	if err != nil || !auth.Granted || r.Definition.Effect != "write" {
		return auth, err
	}
	if r.Call.Arguments != "" {
		in, err := a.prepare(r)
		if err != nil {
			auth.ConfirmationRequired = false
			return auth, nil
		} // executor returns correction, no effect
		if _, _, err := a.access(ctx, r, in); err != nil {
			return sdk.Authorization{}, err
		}
	}
	if auth.ConfirmationRequired {
		return auth, nil
	} // never override a host's extra policy
	auth.ConfirmationRequired = true
	if r.Confirmation == nil || r.ConfirmationID == "" {
		return auth, nil
	}
	valid, err := a.Confirmation.VerifyConversationToolConfirmation(ctx, r)
	if err != nil {
		return sdk.Authorization{}, err
	}
	auth.ConfirmationRequired = !valid
	return auth, nil
}

// Validate the advertised shape before requesting confirmation as well as at
// execution. Typed PATCH decoding alone cannot distinguish null from omission.
func (a *WriteAdapter) prepare(r sdk.Request) (PreparedWrite, error) {
	compiled, err := schema.CompileSchema(r.Definition.InputSchema)
	if err != nil {
		return PreparedWrite{}, err
	}
	if err := schema.ValidateJSON(compiled, []byte(r.Call.Arguments)); err != nil {
		return PreparedWrite{}, err
	}
	return a.Family.Prepare(r.Definition.Key, []byte(r.Call.Arguments))
}

func (a *WriteAdapter) Invoke(ctx context.Context, r sdk.Request) (sdk.Result, error) {
	if r.OutcomeInspectionToken != "" {
		return sdk.Result{}, a.failure("write_access_denied")
	}
	return a.execute(ctx, r, false)
}
func (a *WriteAdapter) Reconcile(ctx context.Context, r sdk.Request) (sdk.Result, error) {
	if r.Definition.Key == a.Family.AccountsKey {
		return a.Invoke(ctx, r)
	}
	return a.execute(ctx, r, true)
}

func (a *WriteAdapter) execute(ctx context.Context, r sdk.Request, receiptOnly bool) (sdk.Result, error) {
	auth, err := a.AuthorizeTool(ctx, r)
	if err != nil || !auth.Granted || auth.ConfirmationRequired {
		return sdk.Result{}, a.failure("write_access_denied")
	}
	if a.Accounts == nil || a.Writes == nil {
		return sdk.Result{}, a.failure("write_unavailable")
	}
	if r.Definition.Key == a.Family.AccountsKey {
		return a.accounts(ctx, r)
	}
	in, err := a.prepare(r)
	if err != nil || in.Present == nil {
		return sdk.Result{Status: "failed", ErrorCode: a.Family.Name + ".write_input_invalid"}, nil
	}
	if strings.TrimSpace(r.IdempotencyKey) == "" || len(r.IdempotencyKey) > 4096 || r.ConversationID == "" || r.RunID == "" || r.Call.ID == "" {
		return sdk.Result{}, a.failure("write_identity_missing")
	}
	s, source, err := a.access(ctx, r, in)
	if err != nil {
		return sdk.Result{}, err
	}
	identity, _ := json.Marshal([]string{r.Authority.RuntimeID, r.IdempotencyKey})
	digest := sha256.Sum256(identity)
	payload, err := json.Marshal(in.Payload)
	if err != nil {
		return sdk.Result{}, err
	}
	request := integration.ConnectionAccountWriteRequest{RequestID: "account-tool:" + hex.EncodeToString(digest[:]), ExpectedSource: source, Payload: payload}
	var out integration.ConnectionAccountWriteResult
	if receiptOnly {
		out, err = a.Writes.ReadConnectionAccountWriteReceipt(ctx, s, in.AccountKey, request)
	} else {
		out, err = a.Writes.WriteConnectionAccount(ctx, s, in.AccountKey, request)
	}
	unknown := func() (sdk.Result, error) {
		return sdk.Result{Status: "uncertain", ErrorCode: a.Family.Name + ".write_result_unknown"}, nil
	}
	if err != nil || out.Source != source {
		return unknown()
	}
	switch out.Status {
	case integration.AccountWriteFailed:
		return sdk.Result{Status: "failed", ErrorCode: a.Family.Name + ".write_rejected"}, nil
	case integration.AccountWriteNotFound, integration.AccountWriteUncertain:
		return unknown()
	case integration.AccountWriteSucceeded:
	default:
		return unknown()
	}
	if out.InvocationID == "" || len(out.InvocationID) > 2048 || len(out.Receipt) > 32<<10 {
		return unknown()
	}
	if _, err := time.Parse(time.RFC3339Nano, out.RecordedAt); err != nil {
		return unknown()
	}
	data, err := in.Present(out.Receipt, out.InvocationID)
	if err != nil {
		return unknown()
	}
	b, err := json.Marshal(data)
	if err != nil {
		return unknown()
	}
	content, err := json.Marshal(writeEnvelope{Data: b, Sources: []integration.ConnectionAccountWriteSource{source}, RecordedAt: out.RecordedAt})
	if err != nil || len(content) > r.Definition.MaxOutputBytes {
		return unknown()
	}
	result := sdk.Result{Status: "completed", Completion: in.Completion, Content: content, ResourceID: out.InvocationID}
	if err := a.AuthorizeResult(ctx, r, result); err != nil {
		return sdk.Result{}, err
	}
	return result, nil
}

type writeEnvelope struct {
	Data       json.RawMessage                            `json:"data"`
	Sources    []integration.ConnectionAccountWriteSource `json:"sources"`
	RecordedAt string                                     `json:"recorded_at,omitempty"`
}

func (a *WriteAdapter) AuthorizeResult(ctx context.Context, r sdk.Request, out sdk.Result) error {
	auth, err := a.Authorize(ctx, r)
	if err != nil || !auth.Granted {
		return a.failure("write_access_denied")
	}
	if out.Status != "completed" {
		return nil
	}
	if a.Accounts == nil || a.Writes == nil || len(out.Content) > 1048576 {
		return a.failure("write_source_invalid")
	}
	var envelope writeEnvelope
	if decode(out.Content, &envelope) != nil {
		return a.failure("write_source_invalid")
	}
	if r.Definition.Key == a.Family.AccountsKey {
		return a.authorizeAccounts(ctx, r, envelope)
	}
	in, err := a.prepare(r)
	if err != nil || out.Completion != in.Completion || out.ResourceID == "" || len(out.ResourceID) > 2048 {
		return a.failure("write_source_invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, envelope.RecordedAt); err != nil {
		return a.failure("write_source_invalid")
	}
	_, source, err := a.access(ctx, r, in)
	if err != nil || len(envelope.Sources) != 1 || envelope.Sources[0] != source {
		return a.failure("write_source_changed")
	}
	if _, err := in.Present(envelope.Data, out.ResourceID); err != nil {
		return a.failure("write_source_invalid")
	}
	return nil
}
