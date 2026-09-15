package accounttools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

// SubjectResolver belongs to the product host. It resolves live identity and
// the exact requested Integration action; model arguments never enter it.
type SubjectResolver func(context.Context, sdk.Authority, string) (integration.ConnectionAccountSubject, error)
type Accounts interface {
	ListConnectionAccounts(context.Context, integration.ConnectionAccountSubject) ([]integration.ConnectionAccount, error)
}

// Ports are supplied by the composition host through public SDK contracts.
type Ports struct {
	Accounts  Accounts
	Reads     integration.ConnectionAccountReads
	Subject   SubjectResolver
	Authorize tools.Authorizer
}

// Family supplies only payload validation/presentation and registered identities.
// Account authorization, discovery and source checks remain shared mechanisms.
type Family struct {
	Name             string
	Definitions      []sdk.Definition
	AccountsKey      string
	DefaultOperation string
	Operations       []string
	// OperationKey maps a public Agent tool key to its owner operation key.
	// Nil preserves the historical one-to-one mapping.
	OperationKey            func(string) string
	OperationSHA256         func(string) string
	Prepare                 func(string, []byte) (Prepared, error)
	IncludeAccountUpdatedAt bool
}
type Prepared struct {
	AccountKey       string
	AccountUpdatedAt string
	Payload          any
	Present          func([]byte) (any, error)
}
type Adapter struct {
	Ports
	Family Family
}

func (f Family) operation(toolKey string) string {
	if f.OperationKey != nil {
		return f.OperationKey(toolKey)
	}
	return toolKey
}

func (f Family) operationSHA256(toolKey string) string {
	return f.OperationSHA256(f.operation(toolKey))
}

func (a *Adapter) Register(reg *tools.Registry) error {
	if (a.Accounts == nil) != (a.Reads == nil) || a.Subject == nil || a.Authorize == nil || reg == nil || a.Family.Name == "" || len(a.Family.Definitions) == 0 || a.Family.OperationSHA256 == nil || a.Family.Prepare == nil {
		return fmt.Errorf("account tool adapter is incomplete")
	}
	for _, d := range a.Family.Definitions {
		if err := reg.Register(tools.Registration{Definition: d, Authorize: a.Authorize, Invoke: a.Invoke, AuthorizeResult: a.AuthorizeResult, AuthorizeResultRead: a.AuthorizeResultRead}); err != nil {
			return err
		}
	}
	return nil
}

type discoveryArguments struct {
	Operation string `json:"operation"`
	Limit     int    `json:"limit"`
	Cursor    string `json:"cursor"`
}

func decode(raw []byte, value any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid account tool data")
	}
	return nil
}

func (a *Adapter) toolError(code string) error {
	return &sdk.Error{Class: "forbidden", Code: a.Family.Name + "." + code}
}

func (a *Adapter) subject(ctx context.Context, authority sdk.Authority, action string) (integration.ConnectionAccountSubject, error) {
	if !authority.Known || authority.RuntimeID == "" || authority.WorkspaceID == "" || authority.UserID == "" {
		return integration.ConnectionAccountSubject{}, a.toolError("account_access_denied")
	}
	s, err := a.Subject(ctx, authority, action)
	if err != nil {
		return integration.ConnectionAccountSubject{}, err
	}
	if s.WorkspaceID != authority.WorkspaceID || s.UserID != authority.UserID || !s.Access.Personal && !s.Access.Workspace {
		return integration.ConnectionAccountSubject{}, a.toolError("account_access_denied")
	}
	return s, nil
}

type envelope struct {
	Data    json.RawMessage                           `json:"data"`
	Sources []integration.ConnectionAccountReadSource `json:"sources"`
	ReadAt  string                                    `json:"read_at,omitempty"`
}

func result(data any, sources []integration.ConnectionAccountReadSource, readAt string) (sdk.Result, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return sdk.Result{}, err
	}
	out, err := json.Marshal(envelope{Data: b, Sources: append([]integration.ConnectionAccountReadSource{}, sources...), ReadAt: readAt})
	if len(out) > 1048576 {
		return sdk.Result{}, fmt.Errorf("account tool response exceeds output limit")
	}
	return sdk.Result{Status: "completed", Content: out}, err
}

func (a *Adapter) Invoke(ctx context.Context, r sdk.Request) (sdk.Result, error) {
	if a.Accounts == nil || a.Reads == nil {
		return sdk.Result{}, a.toolError("account_unavailable")
	}
	authorized, err := a.Authorize(ctx, r)
	if err != nil || !authorized.Granted || authorized.ConfirmationRequired {
		return sdk.Result{}, a.toolError("tool_access_denied")
	}
	if a.Family.AccountsKey != "" && r.Definition.Key == a.Family.AccountsKey {
		var in discoveryArguments
		if err := decode([]byte(r.Call.Arguments), &in); err != nil {
			return sdk.Result{}, err
		}
		return a.accounts(ctx, r, in)
	}
	key := r.Definition.Key
	if a.Family.operationSHA256(key) == "" {
		return sdk.Result{}, a.toolError("operation_invalid")
	}
	prepared, err := a.Family.Prepare(key, []byte(r.Call.Arguments))
	if err != nil {
		return sdk.Result{}, err
	}
	if prepared.AccountKey == "" || prepared.Present == nil {
		return sdk.Result{}, a.toolError("operation_invalid")
	}
	s, err := a.subject(ctx, r.Authority, integration.ActionIntegrationConnectionAccountsRead)
	if err != nil {
		return sdk.Result{}, err
	}
	operation := a.Family.operation(key)
	op := integration.ConnectionAccountReadOperation{Operation: operation, ContractSHA256: a.Family.operationSHA256(key)}
	access, err := a.Reads.AuthorizeConnectionAccountRead(ctx, s, prepared.AccountKey, op)
	if err != nil {
		return sdk.Result{}, a.toolError("account_read_denied")
	}
	if !sourceMatches(access.Source, s, prepared.AccountKey, op) {
		return sdk.Result{}, a.toolError("source_invalid")
	}
	if r.Call.ID == "" || r.RunID == "" {
		return sdk.Result{}, fmt.Errorf("account tool execution identity is required")
	}
	b, err := json.Marshal(prepared.Payload)
	if err != nil {
		return sdk.Result{}, err
	}
	identity, _ := json.Marshal([]string{r.Authority.RuntimeID, r.Authority.WorkspaceID, r.Authority.UserID, r.ConversationID, r.RunID, r.Call.ID, operation, string(b)})
	hash := sha256.Sum256(identity)
	out, err := a.Reads.ReadConnectionAccount(ctx, s, prepared.AccountKey, integration.ConnectionAccountReadRequest{RequestID: a.Family.Name + "-tool:" + hex.EncodeToString(hash[:]), Operation: operation, ContractSHA256: op.ContractSHA256, Payload: b})
	if err != nil {
		return sdk.Result{}, a.toolError("read_failed")
	}
	if !out.PayloadAvailable {
		return sdk.Result{Status: "failed", ErrorCode: a.Family.Name + ".response_not_replayable"}, nil
	}
	if out.Source != access.Source || prepared.AccountUpdatedAt != "" && out.Source.AccountUpdatedAt != prepared.AccountUpdatedAt || len(out.Payload) > 4<<20 {
		return sdk.Result{}, a.toolError("source_invalid")
	}
	data, err := prepared.Present(out.Payload)
	if err != nil {
		return sdk.Result{}, err
	}
	// Resolve live host authorization again after network I/O. The owner also
	// rechecks account state; neither check substitutes for current tool policy.
	auth, err := a.Authorize(ctx, r)
	if err != nil || !auth.Granted || auth.ConfirmationRequired {
		return sdk.Result{}, a.toolError("tool_access_denied")
	}
	s, err = a.subject(ctx, r.Authority, integration.ActionIntegrationConnectionAccountsRead)
	if err != nil {
		return sdk.Result{}, err
	}
	current, err := a.Reads.AuthorizeConnectionAccountRead(ctx, s, prepared.AccountKey, op)
	if err != nil || current.Source != out.Source {
		return sdk.Result{}, a.toolError("source_changed")
	}
	return result(data, []integration.ConnectionAccountReadSource{out.Source}, out.ReadAt)
}

func sourceMatches(source integration.ConnectionAccountReadSource, s integration.ConnectionAccountSubject, key string, op integration.ConnectionAccountReadOperation) bool {
	return source.WorkspaceID == s.WorkspaceID && source.ConnectionKey == key && source.ConnectorKey != "" && source.ProviderKey != "" && source.AccountUpdatedAt != "" && source.Operation == op.Operation && source.ContractSHA256 == op.ContractSHA256
}

type accountSummary struct {
	Key              string `json:"key"`
	Name             string `json:"name"`
	ConnectorKey     string `json:"connector_key"`
	ProviderKey      string `json:"provider_key"`
	AccountUpdatedAt string `json:"account_updated_at,omitempty"`
}
type accountsPage struct {
	Items      []accountSummary `json:"items"`
	Operation  string           `json:"operation"`
	Complete   bool             `json:"complete"`
	NextCursor string           `json:"next_cursor,omitempty"`
}
type accountCursor struct {
	Offset int    `json:"offset"`
	Scope  string `json:"scope"`
}

func (a *Adapter) accounts(ctx context.Context, r sdk.Request, in discoveryArguments) (sdk.Result, error) {
	opKey := in.Operation
	if opKey == "" {
		opKey = a.Family.DefaultOperation
	}
	operation := a.Family.operation(opKey)
	op := integration.ConnectionAccountReadOperation{Operation: operation, ContractSHA256: a.Family.operationSHA256(opKey)}
	if op.ContractSHA256 == "" || in.Limit < 0 || in.Limit > 10 || len(in.Cursor) > 512 {
		return sdk.Result{}, fmt.Errorf("invalid account discovery page")
	}
	listSubject, err := a.subject(ctx, r.Authority, integration.ActionIntegrationConnectionAccountsList)
	if err != nil {
		return sdk.Result{}, err
	}
	readSubject, err := a.subject(ctx, r.Authority, integration.ActionIntegrationConnectionAccountsRead)
	if err != nil {
		return sdk.Result{}, err
	}
	accounts, err := a.Accounts.ListConnectionAccounts(ctx, listSubject)
	if err != nil {
		return sdk.Result{}, a.toolError("account_discovery_failed")
	}
	accounts = append([]integration.ConnectionAccount(nil), accounts...)
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Key < accounts[j].Key })
	identity := []string{listSubject.WorkspaceID, listSubject.UserID, opKey}
	for _, v := range accounts {
		identity = append(identity, v.Key, v.UpdatedAt)
	}
	raw, _ := json.Marshal(identity)
	hash := sha256.Sum256(raw)
	scope := hex.EncodeToString(hash[:])
	offset, limit := 0, in.Limit
	if limit == 0 {
		limit = 5
	}
	if in.Cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		var c accountCursor
		if err != nil || decode(b, &c) != nil || c.Scope != scope || c.Offset < 0 || c.Offset > len(accounts) {
			return sdk.Result{}, fmt.Errorf("account page changed; restart discovery")
		}
		offset = c.Offset
	}
	end := offset + limit
	if end > len(accounts) {
		end = len(accounts)
	}
	page := accountsPage{Items: []accountSummary{}, Operation: opKey, Complete: end == len(accounts)}
	sources := []integration.ConnectionAccountReadSource{}
	for _, v := range accounts[offset:end] {
		if !listedAccount(v, listSubject) {
			continue
		}
		access, err := a.Reads.AuthorizeConnectionAccountRead(ctx, readSubject, v.Key, op)
		if err != nil {
			continue
		}
		if !sourceMatches(access.Source, readSubject, v.Key, op) || access.Source.AccountUpdatedAt != v.UpdatedAt || access.Source.ConnectorKey != v.ConnectorKey || access.Source.ProviderKey != v.ProviderKey {
			return sdk.Result{}, a.toolError("source_changed")
		}
		summary := accountSummary{Key: v.Key, Name: shortText(v.Name, 256), ConnectorKey: v.ConnectorKey, ProviderKey: v.ProviderKey}
		if a.Family.IncludeAccountUpdatedAt {
			summary.AccountUpdatedAt = v.UpdatedAt
		}
		page.Items = append(page.Items, summary)
		sources = append(sources, access.Source)
	}
	if !page.Complete {
		b, _ := json.Marshal(accountCursor{Offset: end, Scope: scope})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	out, err := result(page, sources, "")
	if err != nil {
		return sdk.Result{}, err
	}
	if err = a.AuthorizeResult(ctx, r, out); err != nil {
		return sdk.Result{}, err
	}
	return out, nil
}

func listedAccount(v integration.ConnectionAccount, s integration.ConnectionAccountSubject) bool {
	if v.WorkspaceID != s.WorkspaceID || v.Status != "active" || v.Readiness == nil || !v.Readiness.Available {
		return false
	}
	if v.Scope == integration.ConnectionAccountScopePersonal {
		return s.Access.Personal && v.OwnerUserID == s.UserID
	}
	return v.Scope == integration.ConnectionAccountScopeWorkspace && s.Access.Workspace && v.OwnerUserID == ""
}

func (a *Adapter) AuthorizeResult(ctx context.Context, r sdk.Request, out sdk.Result) error {
	if a.Accounts == nil || a.Reads == nil {
		return a.toolError("account_unavailable")
	}
	auth, err := a.Authorize(ctx, r)
	if err != nil {
		return err
	}
	if !auth.Granted || auth.ConfirmationRequired {
		return a.toolError("tool_access_denied")
	}
	return a.authorizeResultSource(ctx, r, out)
}

// Reading a released result needs the current account data policy, not the
// Agent tool Action. Both policies retain the same source and scope checks.
func (a *Adapter) AuthorizeResultRead(ctx context.Context, r sdk.Request, out sdk.Result) error {
	if out.Status != "completed" || out.Completion != "" || out.ErrorCode != "" || out.ResourceID != "" {
		return a.toolError("source_invalid")
	}
	return a.authorizeResultSource(ctx, r, out)
}

func (a *Adapter) authorizeResultSource(ctx context.Context, r sdk.Request, out sdk.Result) error {
	if a.Accounts == nil || a.Reads == nil || a.Subject == nil {
		return a.toolError("account_unavailable")
	}
	s, err := a.subject(ctx, r.Authority, integration.ActionIntegrationConnectionAccountsRead)
	if err != nil {
		return err
	}
	if out.Status != "completed" {
		return nil
	}
	if len(out.Content) > 1048576 {
		return a.toolError("source_invalid")
	}
	var e envelope
	if decode(out.Content, &e) != nil {
		return a.toolError("source_invalid")
	}
	var in discoveryArguments
	accountKey := ""
	accountUpdatedAt := ""
	if a.Family.AccountsKey != "" && r.Definition.Key == a.Family.AccountsKey {
		if decode([]byte(r.Call.Arguments), &in) != nil {
			return a.toolError("source_invalid")
		}
	} else {
		if a.Family.operationSHA256(r.Definition.Key) == "" {
			return a.toolError("source_invalid")
		}
		prepared, err := a.Family.Prepare(r.Definition.Key, []byte(r.Call.Arguments))
		if err != nil {
			return a.toolError("source_invalid")
		}
		accountKey = prepared.AccountKey
		accountUpdatedAt = prepared.AccountUpdatedAt
	}
	opKey := r.Definition.Key
	if a.Family.AccountsKey != "" && opKey == a.Family.AccountsKey {
		listSubject, err := a.subject(ctx, r.Authority, integration.ActionIntegrationConnectionAccountsList)
		if err != nil {
			return err
		}
		// Discovery names remain governed by the current list policy as well as
		// read policy. A saved result cannot retain a formerly visible scope.
		accounts, err := a.Accounts.ListConnectionAccounts(ctx, listSubject)
		if err != nil {
			return a.toolError("account_discovery_failed")
		}
		visible := map[string]integration.ConnectionAccount{}
		for _, account := range accounts {
			if listedAccount(account, listSubject) {
				visible[account.Key] = account
			}
		}
		var page accountsPage
		if decode(e.Data, &page) != nil || len(page.Items) != len(e.Sources) || len(page.Items) > 10 {
			return a.toolError("source_invalid")
		}
		opKey = in.Operation
		if opKey == "" {
			opKey = a.Family.DefaultOperation
		}
		if page.Operation != opKey {
			return a.toolError("source_invalid")
		}
		for i, v := range page.Items {
			current, found := visible[v.Key]
			if !found || current.UpdatedAt != e.Sources[i].AccountUpdatedAt || current.ConnectorKey != v.ConnectorKey || current.ProviderKey != v.ProviderKey || shortText(current.Name, 256) != v.Name || e.Sources[i].ConnectionKey != v.Key || e.Sources[i].ConnectorKey != v.ConnectorKey || e.Sources[i].ProviderKey != v.ProviderKey || a.Family.IncludeAccountUpdatedAt && v.AccountUpdatedAt != current.UpdatedAt {
				return a.toolError("source_invalid")
			}
		}
	} else if len(e.Sources) != 1 || e.Sources[0].ConnectionKey != accountKey {
		return a.toolError("source_invalid")
	}
	operation := a.Family.operation(opKey)
	op := integration.ConnectionAccountReadOperation{Operation: operation, ContractSHA256: a.Family.operationSHA256(opKey)}
	if op.ContractSHA256 == "" {
		return a.toolError("source_invalid")
	}
	for _, source := range e.Sources {
		if !sourceMatches(source, s, source.ConnectionKey, op) || accountUpdatedAt != "" && source.AccountUpdatedAt != accountUpdatedAt {
			return a.toolError("source_invalid")
		}
		current, err := a.Reads.AuthorizeConnectionAccountRead(ctx, s, source.ConnectionKey, op)
		if err != nil || current.Source != source {
			return a.toolError("source_changed")
		}
	}
	return nil
}

func shortText(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}
