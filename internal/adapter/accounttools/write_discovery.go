package accounttools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
)

type writeAccountSummary struct {
	Key              string `json:"key"`
	Name             string `json:"name"`
	ConnectorKey     string `json:"connector_key"`
	ProviderKey      string `json:"provider_key"`
	AccountUpdatedAt string `json:"account_updated_at"`
}
type writeAccountsPage struct {
	Items      []writeAccountSummary `json:"items"`
	Operation  string                `json:"operation"`
	Complete   bool                  `json:"complete"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

func (a *WriteAdapter) discovery(raw []byte) (discoveryArguments, error) {
	var in discoveryArguments
	if err := decode(raw, &in); err != nil {
		return in, err
	}
	if in.Operation == "" {
		in.Operation = a.Family.DefaultOperation
	}
	if in.Limit == 0 {
		in.Limit = 5
	}
	if a.Family.operationSHA256(in.Operation) == "" || in.Limit < 1 || in.Limit > 10 || len(in.Cursor) > 512 {
		return in, fmt.Errorf("invalid write account discovery")
	}
	return in, nil
}

func (a *WriteAdapter) accounts(ctx context.Context, r sdk.Request) (sdk.Result, error) {
	in, err := a.discovery([]byte(r.Call.Arguments))
	if err != nil {
		return sdk.Result{}, err
	}
	ls, err := a.subject(ctx, r.Authority, integration.ActionIntegrationConnectionAccountsList)
	if err != nil {
		return sdk.Result{}, err
	}
	ws, err := a.subject(ctx, r.Authority, integration.ActionIntegrationConnectionAccountsWrite)
	if err != nil {
		return sdk.Result{}, err
	}
	accounts, err := a.Accounts.ListConnectionAccounts(ctx, ls)
	if err != nil {
		return sdk.Result{}, a.failure("write_account_discovery_failed")
	}
	accounts = append([]integration.ConnectionAccount(nil), accounts...)
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Key < accounts[j].Key })
	identity := []string{ls.WorkspaceID, ls.UserID, in.Operation}
	for _, v := range accounts {
		identity = append(identity, v.Key, v.UpdatedAt)
	}
	raw, _ := json.Marshal(identity)
	digest := sha256.Sum256(raw)
	scope := hex.EncodeToString(digest[:])
	offset := 0
	if in.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		var cursor accountCursor
		if err != nil || decode(raw, &cursor) != nil || cursor.Scope != scope || cursor.Offset < 0 || cursor.Offset > len(accounts) {
			return sdk.Result{}, fmt.Errorf("write accounts changed; restart discovery")
		}
		offset = cursor.Offset
	}
	end := min(len(accounts), offset+in.Limit)
	page := writeAccountsPage{Items: []writeAccountSummary{}, Operation: in.Operation, Complete: end == len(accounts)}
	sources := []integration.ConnectionAccountWriteSource{}
	operation := a.Family.operation(in.Operation)
	op := integration.ConnectionAccountWriteOperation{Operation: operation, ContractSHA256: a.Family.operationSHA256(in.Operation)}
	for _, v := range accounts[offset:end] {
		if !listedAccount(v, ls) {
			continue
		}
		access, err := a.Writes.AuthorizeConnectionAccountWrite(ctx, ws, v.Key, op)
		if err != nil {
			continue
		}
		if !writeSourceMatches(access.Source, ws, v.Key, op) || access.Source.AccountUpdatedAt != v.UpdatedAt || access.Source.ConnectorKey != v.ConnectorKey || access.Source.ProviderKey != v.ProviderKey {
			return sdk.Result{}, a.failure("write_source_changed")
		}
		page.Items = append(page.Items, writeAccountSummary{Key: v.Key, Name: shortText(v.Name, 256), ConnectorKey: v.ConnectorKey, ProviderKey: v.ProviderKey, AccountUpdatedAt: v.UpdatedAt})
		sources = append(sources, access.Source)
	}
	if !page.Complete {
		b, _ := json.Marshal(accountCursor{Offset: end, Scope: scope})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	b, _ := json.Marshal(page)
	content, err := json.Marshal(writeEnvelope{Data: b, Sources: sources})
	if err != nil {
		return sdk.Result{}, err
	}
	out := sdk.Result{Status: "completed", Content: content}
	if err := a.AuthorizeResult(ctx, r, out); err != nil {
		return sdk.Result{}, err
	}
	return out, nil
}

func (a *WriteAdapter) authorizeAccounts(ctx context.Context, r sdk.Request, e writeEnvelope) error {
	return a.authorizeAccountsForAction(ctx, r, e, integration.ActionIntegrationConnectionAccountsWrite)
}

func (a *WriteAdapter) authorizeAccountsForAction(ctx context.Context, r sdk.Request, e writeEnvelope, action string) error {
	in, err := a.discovery([]byte(r.Call.Arguments))
	if err != nil {
		return a.failure("write_source_invalid")
	}
	var page writeAccountsPage
	if decode(e.Data, &page) != nil || page.Operation != in.Operation || len(page.Items) > in.Limit || len(page.Items) != len(e.Sources) {
		return a.failure("write_source_invalid")
	}
	ls, err := a.subject(ctx, r.Authority, integration.ActionIntegrationConnectionAccountsList)
	if err != nil {
		return err
	}
	ws, err := a.subject(ctx, r.Authority, action)
	if err != nil {
		return err
	}
	accounts, err := a.Accounts.ListConnectionAccounts(ctx, ls)
	if err != nil {
		return a.failure("write_account_discovery_failed")
	}
	visible := map[string]integration.ConnectionAccount{}
	for _, v := range accounts {
		if listedAccount(v, ls) {
			visible[v.Key] = v
		}
	}
	operation := a.Family.operation(in.Operation)
	op := integration.ConnectionAccountWriteOperation{Operation: operation, ContractSHA256: a.Family.operationSHA256(in.Operation)}
	seen := map[string]bool{}
	for i, v := range page.Items {
		account, ok := visible[v.Key]
		if !ok || seen[v.Key] || account.UpdatedAt != v.AccountUpdatedAt || account.ConnectorKey != v.ConnectorKey || account.ProviderKey != v.ProviderKey || shortText(account.Name, 256) != v.Name {
			return a.failure("write_source_changed")
		}
		seen[v.Key] = true
		access, err := a.Writes.AuthorizeConnectionAccountWrite(ctx, ws, v.Key, op)
		if err != nil || !writeSourceMatches(access.Source, ws, v.Key, op) || access.Source != e.Sources[i] || access.Source.AccountUpdatedAt != account.UpdatedAt || access.Source.ConnectorKey != account.ConnectorKey || access.Source.ProviderKey != account.ProviderKey {
			return a.failure("write_source_changed")
		}
	}
	return nil
}

func (a *WriteAdapter) ConversationToolAvailable(ctx context.Context, authority sdk.Authority, key string) (bool, error) {
	if a.Accounts == nil || a.Writes == nil || a.Confirmation == nil {
		return false, nil
	}
	keys := []string{key}
	if key == a.Family.AccountsKey {
		keys = a.Family.Operations
	} else if a.Family.operationSHA256(key) == "" {
		return false, nil
	}
	ls, err := a.subject(ctx, authority, integration.ActionIntegrationConnectionAccountsList)
	if err != nil {
		return false, availabilityError(err)
	}
	ws, err := a.subject(ctx, authority, integration.ActionIntegrationConnectionAccountsWrite)
	if err != nil {
		return false, availabilityError(err)
	}
	accounts, err := a.Accounts.ListConnectionAccounts(ctx, ls)
	if err != nil {
		return false, err
	}
	for _, v := range accounts {
		if !listedAccount(v, ls) {
			continue
		}
		for _, opKey := range keys {
			operation := a.Family.operation(opKey)
			op := integration.ConnectionAccountWriteOperation{Operation: operation, ContractSHA256: a.Family.operationSHA256(opKey)}
			access, err := a.Writes.AuthorizeConnectionAccountWrite(ctx, ws, v.Key, op)
			if err == nil && writeSourceMatches(access.Source, ws, v.Key, op) && access.Source.AccountUpdatedAt == v.UpdatedAt && access.Source.ConnectorKey == v.ConnectorKey && access.Source.ProviderKey == v.ProviderKey {
				return true, nil
			}
		}
	}
	return false, nil
}
