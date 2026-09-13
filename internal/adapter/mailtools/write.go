package mailtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/domainry/domainry-connector-sdk/mail"
	"github.com/domainry/domainry-connector-sdk/mailwrite"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools/internal/adapter/accounttools"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

type WriteAdapter accounttools.WritePorts

func (a *WriteAdapter) common() *accounttools.WriteAdapter {
	return &accounttools.WriteAdapter{WritePorts: accounttools.WritePorts(*a), Family: accounttools.WriteFamily{Name: "mail", AccountsKey: WriteAccountsKey, DefaultOperation: mailwrite.SendOperationKey, Definitions: WriteDefinitions(), Operations: []string{mailwrite.SendOperationKey, mailwrite.ReplyOperationKey}, OperationSHA256: mailwrite.OperationSHA256, Prepare: prepareWrite}}
}
func (a *WriteAdapter) Register(reg *tools.Registry) error { return a.common().Register(reg) }
func (a *WriteAdapter) ConversationToolAvailable(ctx context.Context, authority sdk.Authority, key string) (bool, error) {
	return a.common().ConversationToolAvailable(ctx, authority, key)
}

func (a *WriteAdapter) ConversationToolResultReadAvailable(ctx context.Context, authority sdk.Authority, key string) (bool, error) {
	return a.common().ConversationToolResultReadAvailable(ctx, authority, key)
}

func prepareWrite(key string, raw []byte) (accounttools.PreparedWrite, error) {
	var in struct {
		AccountKey       string          `json:"account_key"`
		AccountUpdatedAt string          `json:"account_updated_at"`
		Request          json.RawMessage `json:"request"`
	}
	if len(raw) > 2<<20 || decode(raw, &in) != nil || !mail.ValidID(in.AccountKey) || len(in.AccountKey) > 1024 || in.AccountUpdatedAt == "" || len(in.AccountUpdatedAt) > 128 || strings.TrimSpace(in.AccountUpdatedAt) != in.AccountUpdatedAt {
		return accounttools.PreparedWrite{}, fmt.Errorf("invalid mail write input")
	}
	var payload any
	original := ""
	switch key {
	case mailwrite.SendOperationKey:
		var value mailwrite.SendRequest
		if err := json.Unmarshal(in.Request, &value); err != nil {
			return accounttools.PreparedWrite{}, err
		}
		payload = value
	case mailwrite.ReplyOperationKey:
		var value mailwrite.ReplyRequest
		if err := json.Unmarshal(in.Request, &value); err != nil {
			return accounttools.PreparedWrite{}, err
		}
		payload, original = value, value.MessageID
	default:
		return accounttools.PreparedWrite{}, fmt.Errorf("invalid mail write operation")
	}
	return accounttools.PreparedWrite{AccountKey: in.AccountKey, AccountUpdatedAt: in.AccountUpdatedAt, Payload: payload, Completion: "accepted", Present: func(raw []byte, ref string) (any, error) {
		var result mailwrite.Result
		if len(raw) > 32<<10 || decode(raw, &result) != nil {
			return nil, fmt.Errorf("invalid mail receipt")
		}
		if err := result.Validate(key, ref, original); err != nil {
			return nil, err
		}
		return result, nil
	}}, nil
}
