package mailtools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	mail "github.com/domainry/domainry-connector-sdk/mail"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools/internal/adapter/accounttools"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

type SubjectResolver = accounttools.SubjectResolver
type Accounts = accounttools.Accounts
type Adapter accounttools.Ports

func (a *Adapter) common() *accounttools.Adapter {
	return &accounttools.Adapter{Ports: accounttools.Ports(*a), Family: accounttools.Family{
		Name: "mail", Definitions: Definitions(), AccountsKey: AccountsKey, DefaultOperation: mail.ListOperationKey,
		Operations: []string{mail.ListOperationKey, mail.SearchOperationKey, mail.ReadOperationKey}, OperationSHA256: mail.OperationSHA256, Prepare: prepare,
	}}
}
func (a *Adapter) Register(reg *tools.Registry) error { return a.common().Register(reg) }
func (a *Adapter) Invoke(ctx context.Context, r sdk.Request) (sdk.Result, error) {
	return a.common().Invoke(ctx, r)
}
func (a *Adapter) AuthorizeResult(ctx context.Context, r sdk.Request, out sdk.Result) error {
	return a.common().AuthorizeResult(ctx, r, out)
}
func (a *Adapter) ConversationToolAvailable(ctx context.Context, authority sdk.Authority, key string) (bool, error) {
	return a.common().ConversationToolAvailable(ctx, authority, key)
}

func decode(raw []byte, out any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid mail tool data")
	}
	return nil
}
func prepare(key string, raw []byte) (accounttools.Prepared, error) {
	var out accounttools.Prepared
	switch key {
	case mail.ListOperationKey:
		var in struct {
			AccountKey string `json:"account_key"`
			mail.PageRequest
		}
		if err := decode(raw, &in); err != nil {
			return out, err
		}
		if err := in.PageRequest.Validate(); err != nil {
			return out, err
		}
		out = accounttools.Prepared{AccountKey: in.AccountKey, Payload: in.PageRequest, Present: func(raw []byte) (any, error) { return presentPage(raw, in.PageRequest.PageSize(), "") }}
	case mail.SearchOperationKey:
		var in struct {
			AccountKey string `json:"account_key"`
			mail.SearchRequest
		}
		if err := decode(raw, &in); err != nil {
			return out, err
		}
		if err := in.SearchRequest.Validate(); err != nil {
			return out, err
		}
		out = accounttools.Prepared{AccountKey: in.AccountKey, Payload: in.SearchRequest, Present: func(raw []byte) (any, error) {
			return presentPage(raw, (mail.PageRequest{Limit: in.Limit}).PageSize(), in.QuerySyntax)
		}}
	case mail.ReadOperationKey:
		var in struct {
			AccountKey string `json:"account_key"`
			mail.ReadRequest
		}
		if err := decode(raw, &in); err != nil {
			return out, err
		}
		if err := in.ReadRequest.Validate(); err != nil {
			return out, err
		}
		out = accounttools.Prepared{AccountKey: in.AccountKey, Payload: in.ReadRequest, Present: func(raw []byte) (any, error) { return presentMessage(raw, in.ReadRequest) }}
	default:
		return out, fmt.Errorf("invalid mail operation")
	}
	if !mail.ValidID(out.AccountKey) || len(out.AccountKey) > 1024 {
		return accounttools.Prepared{}, fmt.Errorf("invalid mail account")
	}
	return out, nil
}
