package webtools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/domainry/domainry-connector-sdk/web"
	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools/internal/adapter/accounttools"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

type SubjectResolver = accounttools.SubjectResolver
type Accounts = accounttools.Accounts

// ConnectionKey is a host-selected workspace service connection. It is never
// accepted from model input, and other available connections cannot replace it.
type Adapter struct {
	Accounts      Accounts
	Reads         integration.ConnectionAccountReads
	Subject       SubjectResolver
	Authorize     tools.Authorizer
	ConnectionKey string
}

func validConnectionKey(key string) bool {
	return key != "" && len(key) <= 1024 && utf8.ValidString(key) && !strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

func (a *Adapter) common() *accounttools.Adapter {
	var accounts Accounts
	key := a.ConnectionKey
	if a.Accounts != nil {
		accounts = selectedAccounts{Accounts: a.Accounts, key: key}
	}
	return &accounttools.Adapter{Ports: accounttools.Ports{Accounts: accounts, Reads: a.Reads, Subject: a.workspaceSubject, Authorize: a.Authorize}, Family: accounttools.Family{
		Name: "web", Definitions: Definitions(), Operations: []string{web.SearchOperationKey, web.FetchOperationKey}, OperationSHA256: web.OperationSHA256,
		Prepare: func(operation string, raw []byte) (accounttools.Prepared, error) { return prepare(key, operation, raw) },
	}}
}
func (a *Adapter) Register(reg *tools.Registry) error {
	if a.Subject == nil || a.ConnectionKey != "" && !validConnectionKey(a.ConnectionKey) {
		return fmt.Errorf("web tool host configuration is invalid")
	}
	return a.common().Register(reg)
}
func (a *Adapter) Invoke(ctx context.Context, r sdk.Request) (sdk.Result, error) {
	return a.common().Invoke(ctx, r)
}
func (a *Adapter) AuthorizeResult(ctx context.Context, r sdk.Request, result sdk.Result) error {
	return a.common().AuthorizeResult(ctx, r, result)
}
func (a *Adapter) ConversationToolAvailable(ctx context.Context, authority sdk.Authority, key string) (bool, error) {
	if !validConnectionKey(a.ConnectionKey) {
		return false, nil
	}
	return a.common().ConversationToolAvailable(ctx, authority, key)
}

func (a *Adapter) workspaceSubject(ctx context.Context, authority sdk.Authority, action string) (integration.ConnectionAccountSubject, error) {
	if a.Subject == nil {
		return integration.ConnectionAccountSubject{}, fmt.Errorf("web subject resolver is unavailable")
	}
	s, err := a.Subject(ctx, authority, action)
	if err != nil {
		return s, err
	}
	if !s.Access.Workspace {
		return integration.ConnectionAccountSubject{}, &sdk.Error{Class: "forbidden", Code: "web.account_access_denied"}
	}
	s.Access.Personal = false
	return s, nil
}

type selectedAccounts struct {
	Accounts
	key string
}

func (s selectedAccounts) ListConnectionAccounts(ctx context.Context, subject integration.ConnectionAccountSubject) ([]integration.ConnectionAccount, error) {
	accounts, err := s.Accounts.ListConnectionAccounts(ctx, subject)
	if err != nil {
		return nil, err
	}
	out := []integration.ConnectionAccount{}
	for _, a := range accounts {
		if a.Key == s.key && a.Scope == integration.ConnectionAccountScopeWorkspace {
			out = append(out, a)
		}
	}
	return out, nil
}

func decode(raw []byte, out any) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("invalid web tool data")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid web tool data")
	}
	return nil
}
func prepare(connectionKey, operation string, raw []byte) (accounttools.Prepared, error) {
	var out accounttools.Prepared
	if !validConnectionKey(connectionKey) {
		return out, &sdk.Error{Class: "forbidden", Code: "web.account_unavailable"}
	}
	out.AccountKey = connectionKey
	switch operation {
	case web.SearchOperationKey:
		var input web.SearchRequest
		if decode(raw, &input) != nil || input.Validate() != nil {
			return out, fmt.Errorf("invalid web search input")
		}
		out.Payload = input
		out.Present = func(raw []byte) (any, error) {
			var result web.SearchResult
			if decode(raw, &result) != nil || result.Validate(input) != nil {
				return nil, fmt.Errorf("invalid web search source")
			}
			return struct {
				Query string `json:"query"`
				web.SearchResult
			}{input.Query, result}, nil
		}
	case web.FetchOperationKey:
		var input web.FetchRequest
		if decode(raw, &input) != nil || input.Validate() != nil {
			return out, fmt.Errorf("invalid web fetch input")
		}
		input.URL, _ = web.NormalizeURL(input.URL)
		out.Payload = input
		out.Present = func(raw []byte) (any, error) {
			var page web.Page
			if decode(raw, &page) != nil || page.Validate(input) != nil {
				return nil, fmt.Errorf("invalid web page source")
			}
			return page, nil
		}
	default:
		return out, fmt.Errorf("unknown web operation")
	}
	return out, nil
}
