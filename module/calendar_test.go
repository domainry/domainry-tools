package module_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	calendar "github.com/domainry/domainry-connector-sdk/calendar"
	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	module "github.com/domainry/domainry-tools/module"
)

type calendarFixture struct{ *accountReadFixture }

func newCalendarFixture() *calendarFixture {
	f := &calendarFixture{accountReadFixture: newAccountReadFixture()}
	f.accounts[0].ConnectorKey = "calendar-provider"
	f.accounts[0].ProviderKey = "calendar-provider"
	f.accounts[0].Name = "日历账号"
	f.set(calendar.CalendarsPage{Items: []calendar.Calendar{{ID: "primary", Name: "工作日历"}}, Complete: true})
	return f
}
func (f *calendarFixture) selection(t *testing.T) *module.Selection {
	t.Helper()
	reg := module.NewRegistry()
	a := &module.CalendarAdapter{Accounts: f, Reads: f, Subject: f.subject, Authorize: f.authorize}
	if err := a.Register(reg); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, d := range module.CalendarDefinitions() {
		keys = append(keys, d.Key)
	}
	s, err := reg.Select(keys)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func calendarRequest(t *testing.T, key string, args any) sdk.Request {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	r := sdk.Request{Authority: sdk.Authority{Known: true, RuntimeID: "runtime", WorkspaceID: "workspace", UserID: "alice"}, ConversationID: "conversation", RunID: "run", Call: sdk.Call{ID: "call", Name: key, Arguments: string(b)}}
	for _, d := range module.CalendarDefinitions() {
		if d.Key == key {
			r.Definition = d
			return r
		}
	}
	t.Fatalf("missing definition %s", key)
	return r
}

type calendarOutput struct {
	Data    json.RawMessage                           `json:"data"`
	Sources []integration.ConnectionAccountReadSource `json:"sources"`
	ReadAt  string                                    `json:"read_at"`
}

func invokeCalendar(t *testing.T, s *module.Selection, r sdk.Request) (sdk.Result, calendarOutput) {
	t.Helper()
	out, err := s.InvokeConversationTool(context.Background(), r)
	if err != nil || out.Status != "completed" {
		t.Fatalf("invoke: %+v %v", out, err)
	}
	var envelope calendarOutput
	if err = json.Unmarshal(out.Content, &envelope); err != nil {
		t.Fatal(err)
	}
	return out, envelope
}
func testWindow() calendar.Window {
	return calendar.Window{Start: "2026-11-01T00:00:00-04:00", End: "2026-11-02T00:00:00-05:00"}
}
func eventArgs(key string) map[string]any {
	a := map[string]any{"account_key": "account", "calendar_id": "primary", "time_zone": "America/New_York"}
	if key == calendar.EventOperationKey {
		a["event_id"] = "event"
	} else {
		a["window"] = testWindow()
	}
	return a
}
func testEvent() calendar.Event {
	return calendar.Event{ID: "event", CalendarID: "primary", Title: "全天活动", Start: calendar.Moment{Date: "2026-11-01"}, End: calendar.Moment{Date: "2026-11-02"}, SeriesID: "series", OriginalStart: &calendar.Moment{Date: "2026-10-25"}}
}

func TestCalendarPublicToolsReadContracts(t *testing.T) {
	for _, key := range []string{calendar.ListOperationKey, calendar.EventsOperationKey, calendar.EventOperationKey, calendar.AvailabilityOperationKey} {
		t.Run(key, func(t *testing.T) {
			f := newCalendarFixture()
			var args any = map[string]any{"account_key": "account"}
			switch key {
			case calendar.EventsOperationKey:
				f.set(calendar.EventsPage{Items: []calendar.Event{testEvent()}, Complete: false, NextCursor: "next", TimeZone: "America/New_York"})
				args = eventArgs(key)
			case calendar.EventOperationKey:
				f.set(testEvent())
				args = eventArgs(key)
			case calendar.AvailabilityOperationKey:
				v, err := calendar.ResolveAvailability(calendar.AvailabilityRequest{CalendarIDs: []string{"primary"}, Window: testWindow(), TimeZone: "America/New_York"}, []calendar.CalendarBusy{{CalendarID: "primary", Busy: []calendar.Window{}, Complete: true}})
				if err != nil {
					t.Fatal(err)
				}
				f.set(v)
				args = map[string]any{"account_key": "account", "calendar_ids": []string{"primary"}, "window": testWindow(), "time_zone": "America/New_York"}
			}
			s := f.selection(t)
			r := calendarRequest(t, key, args)
			out, e := invokeCalendar(t, s, r)
			if len(f.requests) != 1 || f.requests[0].Validate() != nil || f.requests[0].ContractSHA256 != calendar.OperationSHA256(key) || f.requests[0].Operation != key {
				t.Fatalf("owner request %+v", f.requests)
			}
			for _, forbidden := range []string{"account_key", "workspace_id", "user_id", "access", "scope", "secret"} {
				if strings.Contains(string(f.requests[0].Payload), forbidden) {
					t.Fatalf("model authority forwarded: %s", f.requests[0].Payload)
				}
			}
			if len(e.Sources) != 1 || e.Sources[0].ConnectionKey != "account" || e.ReadAt == "" {
				t.Fatalf("source missing %+v", e)
			}
			if err := s.AuthorizeConversationToolResult(context.Background(), r, out); err != nil {
				t.Fatal(err)
			}
			if key == calendar.EventsOperationKey || key == calendar.EventOperationKey {
				if !strings.Contains(string(e.Data), `"date":"2026-11-01"`) || strings.Contains(string(e.Data), `"date_time"`) {
					t.Fatalf("all-day dates changed: %s", e.Data)
				}
			}
			if key == calendar.AvailabilityOperationKey && !strings.Contains(string(e.Data), `2026-11-02T05:00:00Z`) {
				t.Fatalf("DST window changed: %s", e.Data)
			}
		})
	}
}

func TestCalendarAvailabilityUsesCurrentOperationContract(t *testing.T) {
	f := newCalendarFixture()
	a := &module.CalendarAdapter{Accounts: f, Reads: f, Subject: f.subject, Authorize: f.authorize}
	authority := calendarRequest(t, calendar.ListOperationKey, map[string]any{}).Authority
	for _, key := range []string{calendar.ListOperationKey, calendar.EventsOperationKey, calendar.EventOperationKey} {
		f.blocked["account:"+key] = true
	}
	for _, tt := range []struct {
		key  string
		want bool
	}{{"calendar_accounts", true}, {calendar.AvailabilityOperationKey, true}, {calendar.EventOperationKey, false}, {"unknown", false}} {
		got, err := a.ConversationToolAvailable(context.Background(), authority, tt.key)
		if err != nil || got != tt.want {
			t.Fatalf("%s: %v %v", tt.key, got, err)
		}
	}
	if len(f.requests) != 0 {
		t.Fatal("preflight performed vendor I/O")
	}
	f.readAccess = integration.ConnectionAccountAccess{}
	if available, err := a.ConversationToolAvailable(context.Background(), authority, "calendar_accounts"); err != nil || available {
		t.Fatal("known account denial was treated as a service failure", err)
	}
	a.Accounts = nil
	a.Reads = nil
	if err := a.Register(module.NewRegistry()); err != nil {
		t.Fatal("unconfigured product cannot expose settings", err)
	}
	if available, err := a.ConversationToolAvailable(context.Background(), authority, "calendar_accounts"); err != nil || available {
		t.Fatal("unconfigured available", err)
	}
	if out, err := a.Invoke(context.Background(), calendarRequest(t, calendar.ListOperationKey, map[string]any{"account_key": "account"})); err == nil || len(out.Content) > 0 {
		t.Fatal("unconfigured execution succeeded")
	}
}

func TestCalendarDiscoveryScopesPagingAndRetainedAuthorization(t *testing.T) {
	f := newCalendarFixture()
	personal := f.accounts[0]
	shared := personal
	shared.Key = "z-shared"
	shared.Scope = integration.ConnectionAccountScopeWorkspace
	shared.OwnerUserID = ""
	other := personal
	other.Key = "b-other"
	other.OwnerUserID = "bob"
	blocked := personal
	blocked.Key = "c-blocked"
	f.blocked[blocked.Key+":"+calendar.EventOperationKey] = true
	f.accounts = []integration.ConnectionAccount{shared, personal, other, blocked}
	s := f.selection(t)
	r := calendarRequest(t, "calendar_accounts", map[string]any{"operation": calendar.EventOperationKey, "limit": 2})
	out, e := invokeCalendar(t, s, r)
	var page struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
		Complete   bool   `json:"complete"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(e.Data, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Key != "account" || page.Complete || page.NextCursor == "" || f.accounts[0].Key != "z-shared" {
		t.Fatalf("page/isolation/mutation: %s", e.Data)
	}
	next := calendarRequest(t, "calendar_accounts", map[string]any{"operation": calendar.EventOperationKey, "limit": 2, "cursor": page.NextCursor})
	_, e2 := invokeCalendar(t, s, next)
	if err := json.Unmarshal(e2.Data, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Key != "z-shared" || !page.Complete {
		t.Fatalf("second page %s", e2.Data)
	}
	f.listAccess.Personal = false
	if err := s.AuthorizeConversationToolResult(context.Background(), r, out); err == nil {
		t.Fatal("retained personal discovery survived list-scope removal")
	}
	f.listAccess.Personal = true
	f.accounts[0].UpdatedAt = "revision-2"
	if _, err := s.InvokeConversationTool(context.Background(), next); err == nil {
		t.Fatal("changed account catalog accepted old cursor")
	}
	if len(f.requests) != 0 {
		t.Fatal("discovery performed a vendor read")
	}
}

func TestCalendarRejectsInvalidModelInputBeforeOwnerRead(t *testing.T) {
	cases := []struct{ name, key, raw string }{
		{"identity", calendar.ListOperationKey, `{"account_key":"account","user_id":"bob"}`},
		{"scope", calendar.ListOperationKey, `{"account_key":"account","scope":"all"}`},
		{"oversized_page", calendar.ListOperationKey, `{"account_key":"account","limit":26}`},
		{"foreign_argument", calendar.ListOperationKey, `{"account_key":"account","event_id":"event"}`},
		{"naive_window", calendar.EventsOperationKey, `{"account_key":"account","calendar_id":"primary","time_zone":"UTC","window":{"start":"2026-09-11T10:00:00","end":"2026-09-11T11:00:00"}}`},
		{"local_zone", calendar.EventOperationKey, `{"account_key":"account","calendar_id":"primary","event_id":"event","time_zone":"Local"}`},
		{"duplicate_calendars", calendar.AvailabilityOperationKey, `{"account_key":"account","calendar_ids":["primary","primary"],"time_zone":"UTC","window":{"start":"2026-09-11T10:00:00Z","end":"2026-09-11T11:00:00Z"}}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := newCalendarFixture()
			s := f.selection(t)
			r := calendarRequest(t, tt.key, map[string]any{})
			r.Call.Arguments = tt.raw
			if _, err := s.InvokeConversationTool(context.Background(), r); err == nil {
				t.Fatal("invalid input accepted")
			}
			if len(f.requests) > 0 {
				t.Fatal("owner called")
			}
		})
	}
}

func TestCalendarCurrentAuthorizationAndSourceChanges(t *testing.T) {
	for _, mode := range []string{"tool_before", "subject_mismatch", "other_user", "other_workspace", "tool_after", "account_after", "read_scope_after", "revision_after", "source_hash", "source_account", "source_revision"} {
		t.Run(mode, func(t *testing.T) {
			f := newCalendarFixture()
			s := f.selection(t)
			r := calendarRequest(t, calendar.ListOperationKey, map[string]any{"account_key": "account"})
			switch mode {
			case "tool_before":
				f.toolAllowed = false
			case "subject_mismatch":
				f.mismatchSubject = true
			case "other_user":
				r.Authority.UserID = "bob"
			case "other_workspace":
				r.Authority.WorkspaceID = "other"
			case "tool_after":
				f.afterRead = func() { f.toolAllowed = false }
			case "account_after":
				f.afterRead = func() { f.accounts[0].Status = "revoked" }
			case "read_scope_after":
				f.afterRead = func() { f.readAccess.Personal = false }
			case "revision_after":
				f.afterRead = func() { f.accounts[0].UpdatedAt = "changed" }
			case "source_hash":
				f.mutateSource = func(s *integration.ConnectionAccountReadSource) { s.ContractSHA256 = strings.Repeat("0", 64) }
			case "source_account":
				f.mutateSource = func(s *integration.ConnectionAccountReadSource) { s.ConnectionKey = "other" }
			case "source_revision":
				f.mutateSource = func(s *integration.ConnectionAccountReadSource) { s.AccountUpdatedAt = "other" }
			}
			out, err := s.InvokeConversationTool(context.Background(), r)
			if err == nil || len(out.Content) > 0 {
				t.Fatalf("changed authority/source returned data: %+v %v", out, err)
			}
			if strings.HasSuffix(mode, "before") || mode == "subject_mismatch" || mode == "other_user" || mode == "other_workspace" {
				if len(f.requests) != 0 {
					t.Fatal("denied request performed read")
				}
			}
		})
	}
	f := newCalendarFixture()
	s := f.selection(t)
	r := calendarRequest(t, calendar.ListOperationKey, map[string]any{"account_key": "account"})
	out, _ := invokeCalendar(t, s, r)
	for _, change := range []func(){func() { f.toolAllowed = false }, func() { f.accounts[0].Status = "revoked" }, func() { f.accounts[0].UpdatedAt = "changed" }, func() { f.readAccess.Personal = false }} {
		change()
		if err := s.AuthorizeConversationToolResult(context.Background(), r, out); err == nil {
			t.Fatal("saved result authorization was not current")
		}
		f.toolAllowed = true
		f.accounts[0].Status = "active"
		f.accounts[0].UpdatedAt = "revision-1"
		f.readAccess.Personal = true
	}
}

func TestCalendarExecutionIdentityAndSensitiveReplay(t *testing.T) {
	f := newCalendarFixture()
	s := f.selection(t)
	r := calendarRequest(t, calendar.ListOperationKey, map[string]any{"account_key": "account"})
	invokeCalendar(t, s, r)
	invokeCalendar(t, s, r)
	if f.requests[0].RequestID != f.requests[1].RequestID {
		t.Fatal("execution retry identity changed")
	}
	r.Call.ID = "call-2"
	invokeCalendar(t, s, r)
	if f.requests[1].RequestID == f.requests[2].RequestID {
		t.Fatal("separate reads collided")
	}
	f.accounts[0].Scope = integration.ConnectionAccountScopeWorkspace
	f.accounts[0].OwnerUserID = ""
	r.Authority.UserID = "bob"
	invokeCalendar(t, s, r)
	if f.requests[2].RequestID == f.requests[3].RequestID {
		t.Fatal("separate actors collided")
	}
	f.replay = true
	out, err := s.InvokeConversationTool(context.Background(), r)
	if err != nil || out.Status != "failed" || out.ErrorCode != "calendar.response_not_replayable" || len(out.Content) != 0 {
		t.Fatalf("replay invented payload %+v %v", out, err)
	}
}

func TestCalendarIncompleteAvailabilityAndPresentationBounds(t *testing.T) {
	t.Run("unknown is not free", func(t *testing.T) {
		f := newCalendarFixture()
		request := calendar.AvailabilityRequest{CalendarIDs: []string{"primary"}, Window: testWindow(), TimeZone: "America/New_York"}
		v, err := calendar.ResolveAvailability(request, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.set(v)
		s := f.selection(t)
		r := calendarRequest(t, calendar.AvailabilityOperationKey, map[string]any{"account_key": "account", "calendar_ids": request.CalendarIDs, "window": request.Window, "time_zone": request.TimeZone})
		_, out := invokeCalendar(t, s, r)
		var data struct {
			Complete bool              `json:"complete"`
			Free     []calendar.Window `json:"free"`
		}
		if err = json.Unmarshal(out.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Complete || len(data.Free) != 0 {
			t.Fatalf("invented availability %s", out.Data)
		}
		v.Free = []calendar.Window{testWindow()}
		f.set(v)
		if _, err = s.InvokeConversationTool(context.Background(), r); err == nil {
			t.Fatal("unverified free intervals accepted")
		}
	})
	t.Run("free page continuation", func(t *testing.T) {
		f := newCalendarFixture()
		start, _ := time.Parse(time.RFC3339, "2026-09-11T00:00:00Z")
		window := calendar.Window{Start: start.Format(time.RFC3339), End: start.Add(24 * time.Hour).Format(time.RFC3339)}
		busy := []calendar.Window{}
		for i := 0; i < 60; i++ {
			a := start.Add(time.Duration(2*i+1) * time.Minute)
			busy = append(busy, calendar.Window{Start: a.Format(time.RFC3339), End: a.Add(time.Minute).Format(time.RFC3339)})
		}
		request := calendar.AvailabilityRequest{CalendarIDs: []string{"primary"}, Window: window, TimeZone: "UTC"}
		v, err := calendar.ResolveAvailability(request, []calendar.CalendarBusy{{CalendarID: "primary", Complete: true, Busy: busy}})
		if err != nil {
			t.Fatal(err)
		}
		f.set(v)
		_, out := invokeCalendar(t, f.selection(t), calendarRequest(t, calendar.AvailabilityOperationKey, map[string]any{"account_key": "account", "calendar_ids": request.CalendarIDs, "window": window, "time_zone": "UTC"}))
		var data struct {
			Complete bool              `json:"complete"`
			Free     []calendar.Window `json:"free"`
			Next     string            `json:"next_window_start"`
		}
		if err = json.Unmarshal(out.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Complete || len(data.Free) != 50 || data.Next != data.Free[49].End || !reflect.DeepEqual(data.Free, v.Free[:50]) {
			t.Fatalf("invalid continuation %s", out.Data)
		}
	})
	t.Run("event bounds and offsets", func(t *testing.T) {
		f := newCalendarFixture()
		event := testEvent()
		event.Title = strings.Repeat("日", 2000)
		event.Description = strings.Repeat("文", 30000)
		event.Location = strings.Repeat("地", 4000)
		event.Start = calendar.Moment{DateTime: "2026-11-01T01:30:00-04:00"}
		event.End = calendar.Moment{DateTime: "2026-11-01T01:30:00-05:00"}
		event.OriginalStart = nil
		f.set(event)
		_, out := invokeCalendar(t, f.selection(t), calendarRequest(t, calendar.EventOperationKey, eventArgs(calendar.EventOperationKey)))
		var data struct {
			calendar.Event
			TextTruncated        bool `json:"text_truncated"`
			DescriptionTruncated bool `json:"description_truncated"`
		}
		if err := json.Unmarshal(out.Data, &data); err != nil {
			t.Fatal(err)
		}
		if !data.TextTruncated || !data.DescriptionTruncated || len([]rune(data.Description)) != 16000 || data.Start != event.Start || data.End != event.End {
			t.Fatalf("bounds/time lost: %d %+v", len(out.Data), data.Start)
		}
		events := make([]calendar.Event, 25)
		for i := range events {
			events[i] = event
			events[i].ID = fmt.Sprint(i)
		}
		f.set(calendar.EventsPage{Items: events, Complete: true})
		result, _ := invokeCalendar(t, f.selection(t), calendarRequest(t, calendar.EventsOperationKey, eventArgs(calendar.EventsOperationKey)))
		if len(result.Content) > 1048576 {
			t.Fatal("page exceeds output limit")
		}
	})
}
