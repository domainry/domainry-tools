package calendartools

import (
	"context"
	"encoding/json"
	"fmt"
	calendar "github.com/domainry/domainry-connector-sdk/calendar"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools/internal/adapter/accounttools"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
	"io"
	"strings"
)

type SubjectResolver = accounttools.SubjectResolver
type Accounts = accounttools.Accounts
type Adapter accounttools.Ports

func (a *Adapter) common() *accounttools.Adapter {
	return &accounttools.Adapter{Ports: accounttools.Ports(*a), Family: accounttools.Family{
		Name: "calendar", Definitions: Definitions(), AccountsKey: AccountsKey, DefaultOperation: calendar.ListOperationKey,
		Operations:      []string{calendar.ListOperationKey, calendar.EventsOperationKey, calendar.EventOperationKey, calendar.AvailabilityOperationKey},
		OperationSHA256: calendar.OperationSHA256, Prepare: prepare,
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

type arguments struct {
	AccountKey  string          `json:"account_key"`
	Operation   string          `json:"operation"`
	CalendarID  string          `json:"calendar_id"`
	CalendarIDs []string        `json:"calendar_ids"`
	EventID     string          `json:"event_id"`
	Window      calendar.Window `json:"window"`
	TimeZone    string          `json:"time_zone"`
	Limit       int             `json:"limit"`
	Cursor      string          `json:"cursor"`
}

func decode(raw []byte, value any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid calendar tool data")
	}
	return nil
}

func prepare(key string, raw []byte) (accounttools.Prepared, error) {
	var in arguments
	if err := decode(raw, &in); err != nil {
		return accounttools.Prepared{}, err
	}
	if calendar.OperationSHA256(key) == "" || !calendar.ValidID(in.AccountKey) {
		return accounttools.Prepared{}, fmt.Errorf("invalid calendar operation")
	}
	var payload any
	switch key {
	case calendar.ListOperationKey:
		payload = calendar.PageRequest{Limit: in.Limit, Cursor: in.Cursor}
	case calendar.EventsOperationKey:
		payload = calendar.EventsRequest{CalendarID: in.CalendarID, Window: in.Window, TimeZone: in.TimeZone, Limit: in.Limit, Cursor: in.Cursor}
	case calendar.EventOperationKey:
		payload = calendar.EventRequest{CalendarID: in.CalendarID, EventID: in.EventID, TimeZone: in.TimeZone}
	case calendar.AvailabilityOperationKey:
		payload = calendar.AvailabilityRequest{CalendarIDs: in.CalendarIDs, Window: in.Window, TimeZone: in.TimeZone}
	}
	if err := payload.(interface{ Validate() error }).Validate(); err != nil {
		return accounttools.Prepared{}, err
	}

	return accounttools.Prepared{AccountKey: in.AccountKey, Payload: payload, Present: func(raw []byte) (any, error) { return present(key, in, raw) }}, nil
}
