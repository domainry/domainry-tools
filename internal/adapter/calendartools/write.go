package calendartools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/domainry/domainry-connector-sdk/calendar"
	"github.com/domainry/domainry-connector-sdk/calendarwrite"
	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools/internal/adapter/accounttools"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

type WriteAdapter struct {
	Accounts     Accounts
	Reads        integration.ConnectionAccountReads
	Writes       integration.ConnectionAccountWrites
	Subject      SubjectResolver
	Authorize    tools.Authorizer
	Confirmation sdk.ConfirmationVerifier
}

func writeSHA(key string) string {
	if key != calendarwrite.CreateOperationKey && key != calendarwrite.UpdateOperationKey {
		return ""
	}
	return calendarwrite.OperationSHA256(key)
}
func (a *WriteAdapter) common() *accounttools.WriteAdapter {
	return &accounttools.WriteAdapter{WritePorts: accounttools.WritePorts{Accounts: a.Accounts, Writes: a.Writes, Subject: a.Subject, Authorize: a.Authorize, Confirmation: a.Confirmation}, Family: accounttools.WriteFamily{Name: "calendar", AccountsKey: WriteAccountsKey, DefaultOperation: calendarwrite.CreateOperationKey, Definitions: writeDefinitions(), Operations: []string{calendarwrite.CreateOperationKey, calendarwrite.UpdateOperationKey}, OperationSHA256: writeSHA, Prepare: prepareWrite}}
}
func (a *WriteAdapter) inspector() *accounttools.Adapter {
	return &accounttools.Adapter{Ports: accounttools.Ports{Accounts: a.Accounts, Reads: a.Reads, Subject: a.Subject, Authorize: a.Authorize}, Family: accounttools.Family{Name: "calendar", Definitions: []sdk.Definition{inspectDefinition()}, Operations: []string{calendarwrite.InspectOperationKey}, OperationSHA256: func(key string) string {
		if key == calendarwrite.InspectOperationKey {
			return calendarwrite.OperationSHA256(key)
		}
		return ""
	}, Prepare: prepareInspect}}
}
func (a *WriteAdapter) Register(reg *tools.Registry) error {
	if err := a.common().Register(reg); err != nil {
		return err
	}
	return a.inspector().Register(reg)
}
func (a *WriteAdapter) ConversationToolAvailable(ctx context.Context, authority sdk.Authority, key string) (bool, error) {
	if key == calendarwrite.InspectOperationKey {
		return a.inspector().ConversationToolAvailable(ctx, authority, key)
	}
	return a.common().ConversationToolAvailable(ctx, authority, key)
}

func (a *WriteAdapter) ConversationToolResultReadAvailable(ctx context.Context, authority sdk.Authority, key string) (bool, error) {
	if key == calendarwrite.InspectOperationKey {
		return a.inspector().ConversationToolAvailable(ctx, authority, key)
	}
	return a.common().ConversationToolResultReadAvailable(ctx, authority, key)
}

func prepareWrite(key string, raw []byte) (accounttools.PreparedWrite, error) {
	var in struct {
		AccountKey       string          `json:"account_key"`
		AccountUpdatedAt string          `json:"account_updated_at"`
		Request          json.RawMessage `json:"request"`
	}
	if len(raw) > 2<<20 || decode(raw, &in) != nil || !calendar.ValidID(in.AccountKey) || len(in.AccountKey) > 1024 || in.AccountUpdatedAt == "" || len(in.AccountUpdatedAt) > 128 || strings.TrimSpace(in.AccountUpdatedAt) != in.AccountUpdatedAt {
		return accounttools.PreparedWrite{}, fmt.Errorf("invalid calendar write input")
	}
	var payload any
	var calendarID, eventID string
	switch key {
	case calendarwrite.CreateOperationKey:
		var value calendarwrite.CreateRequest
		if err := json.Unmarshal(in.Request, &value); err != nil {
			return accounttools.PreparedWrite{}, err
		}
		payload, calendarID = value, value.CalendarID
	case calendarwrite.UpdateOperationKey:
		var value calendarwrite.UpdateRequest
		if err := json.Unmarshal(in.Request, &value); err != nil {
			return accounttools.PreparedWrite{}, err
		}
		payload, calendarID, eventID = value, value.CalendarID, value.EventID
	default:
		return accounttools.PreparedWrite{}, fmt.Errorf("invalid calendar write operation")
	}
	return accounttools.PreparedWrite{AccountKey: in.AccountKey, AccountUpdatedAt: in.AccountUpdatedAt, Payload: payload, Present: func(raw []byte, ref string) (any, error) {
		var out calendarwrite.Result
		if len(raw) > 32<<10 || decode(raw, &out) != nil {
			return nil, fmt.Errorf("invalid calendar write receipt")
		}
		if err := out.Validate(key, ref, calendarID, eventID); err != nil {
			return nil, err
		}
		return out, nil
	}}, nil
}

func prepareInspect(key string, raw []byte) (accounttools.Prepared, error) {
	var in struct {
		AccountKey string `json:"account_key"`
		CalendarID string `json:"calendar_id"`
		EventID    string `json:"event_id"`
		TimeZone   string `json:"time_zone"`
	}
	if decode(raw, &in) != nil || key != calendarwrite.InspectOperationKey || !calendar.ValidID(in.AccountKey) || len(in.AccountKey) > 1024 {
		return accounttools.Prepared{}, fmt.Errorf("invalid calendar inspection")
	}
	request := calendarwrite.InspectRequest{CalendarID: in.CalendarID, EventID: in.EventID, TimeZone: in.TimeZone}
	if err := request.Validate(); err != nil {
		return accounttools.Prepared{}, err
	}
	return accounttools.Prepared{AccountKey: in.AccountKey, Payload: request, Present: func(raw []byte) (any, error) {
		var snapshot calendarwrite.Snapshot
		if decode(raw, &snapshot) != nil {
			return nil, fmt.Errorf("invalid calendar snapshot")
		}
		if err := snapshot.Validate(request); err != nil {
			return nil, err
		}
		return snapshot, nil
	}}, nil
}
