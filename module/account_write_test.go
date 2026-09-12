package module_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/domainry/domainry-connector-sdk/calendar"
	"github.com/domainry/domainry-connector-sdk/calendarwrite"
	"github.com/domainry/domainry-connector-sdk/mailwrite"
	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	module "github.com/domainry/domainry-tools/module"
)

func TestAccountWritePublicContractsAndOriginalReceipt(t *testing.T) {
	for _, key := range []string{calendarwrite.CreateOperationKey, calendarwrite.UpdateOperationKey, mailwrite.SendOperationKey, mailwrite.ReplyOperationKey} {
		t.Run(key, func(t *testing.T) {
			f := newAccountWriteFixture()
			s := f.selection(t)
			r := writeRequest(t, key, writePayload(key))
			out, err := s.InvokeConversationTool(t.Context(), r)
			if err != nil || out.Status != "completed" || len(f.writes) != 1 {
				t.Fatalf("write %+v %v", out, err)
			}
			request := f.writes[0]
			sha := calendarwrite.OperationSHA256(key)
			if strings.HasPrefix(key, "mail_") {
				sha = mailwrite.OperationSHA256(key)
				if out.Completion != "accepted" {
					t.Fatal("acceptance not preserved")
				}
			}
			if request.ExpectedSource.Operation != key || request.ExpectedSource.ContractSHA256 != sha || request.ExpectedSource.AccountUpdatedAt != "revision-1" || !strings.HasPrefix(request.RequestID, "account-tool:") {
				t.Fatalf("owner envelope %+v", request)
			}
			var want, got any
			_ = json.Unmarshal([]byte(writePayload(key)), &want)
			_ = json.Unmarshal(request.Payload, &got)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("targets/content changed: %s", request.Payload)
			}
			if f.writeSubjects[0].UserID != "alice" || !f.writeSubjects[0].Access.Personal {
				t.Fatal("trusted subject lost")
			}
			for _, forbidden := range []string{"secret", "account_key", "request_id", "workspace_id", "confirmation"} {
				if strings.Contains(string(request.Payload), forbidden) {
					t.Fatalf("authority forwarded into business payload: %s", request.Payload)
				}
			}
			replayed, err := s.ReconcileConversationTool(t.Context(), r)
			if err != nil || !reflect.DeepEqual(out, replayed) || len(f.writes) != 1 || len(f.lookups) != 1 || !reflect.DeepEqual(request, f.lookups[0]) {
				t.Fatalf("receipt replay changed identity or resent: %+v %v", replayed, err)
			}
			// Historical presentation has current read policy, no live lease and
			// no authorization to execute the completed operation again.
			r.Confirmation = nil
			r.ConfirmationID = ""
			r.IdempotencyKey = ""
			if err := s.AuthorizeConversationToolResult(t.Context(), r, out); err != nil {
				t.Fatal(err)
			}
			f.accounts[0].UpdatedAt = "reauthorized-account"
			if err := s.AuthorizeConversationToolResult(t.Context(), r, out); err == nil {
				t.Fatal("retained receipt survived account replacement")
			}
		})
	}
}

func TestAccountWriteRequiresHostApprovalAndCurrentAuthority(t *testing.T) {
	for _, scenario := range []string{"missing_confirmation", "forged_confirmation", "extra_host_policy", "no_identity", "foreign_user", "subject_mismatch", "no_write_permission", "old_revision", "revoked_grant", "tool_disabled", "missing_execution_id", "missing_run", "changed_definition"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAccountWriteFixture()
			s := f.selection(t)
			r := writeRequest(t, mailwrite.SendOperationKey, writePayload(mailwrite.SendOperationKey))
			switch scenario {
			case "missing_confirmation":
				r.Confirmation = nil
			case "forged_confirmation":
				f.approved = false
			case "extra_host_policy":
				f.extraConfirmation = true
			case "no_identity":
				r.Authority.Known = false
			case "foreign_user":
				r.Authority.UserID = "bob"
			case "subject_mismatch":
				f.mismatchSubject = true
			case "no_write_permission":
				f.writeAccess = integration.ConnectionAccountAccess{}
			case "old_revision":
				f.accounts[0].UpdatedAt = "new-revision"
			case "revoked_grant":
				f.blocked["account:"+r.Definition.Key] = true
			case "tool_disabled":
				f.toolAllowed = false
			case "missing_execution_id":
				r.IdempotencyKey = ""
			case "missing_run":
				r.RunID = ""
			case "changed_definition":
				r.Definition.Version = "2"
			}
			if out, err := s.InvokeConversationTool(t.Context(), r); err == nil || len(out.Content) != 0 || len(f.writes) != 0 || len(f.lookups) != 0 {
				t.Fatalf("denied write entered owner: %+v %v", out, err)
			}
		})
	}
	for _, family := range []string{"calendar", "mail"} {
		t.Run("missing_port_"+family, func(t *testing.T) {
			f := newAccountWriteFixture()
			c, m := f.adapters()
			c.Confirmation = nil
			m.Confirmation = nil
			var err error
			if family == "calendar" {
				err = c.Register(module.NewRegistry())
			} else {
				err = m.Register(module.NewRegistry())
			}
			if err == nil {
				t.Fatal("registered without durable approval owner")
			}
		})
	}
}

func TestAccountWriteRejectsInvalidTypedContentBeforeEffect(t *testing.T) {
	cases := map[string]struct{ key, payload string }{
		"calendar_wrong_offset":    {calendarwrite.CreateOperationKey, strings.Replace(calendarCreatePayload, "01:30:00-04:00", "01:30:00+08:00", 1)},
		"calendar_null_attendees":  {calendarwrite.UpdateOperationKey, strings.Replace(calendarUpdatePayload, `"attendees":[]`, `"attendees":null`, 1)},
		"calendar_empty_patch":     {calendarwrite.UpdateOperationKey, strings.Replace(calendarUpdatePayload, `{"description":"","location":"","attendees":[]}`, `{}`, 1)},
		"calendar_invalid_etag":    {calendarwrite.UpdateOperationKey, strings.Replace(calendarUpdatePayload, `\"etag-1\"`, `stale`, 1)},
		"mail_header_injection":    {mailwrite.SendOperationKey, strings.Replace(writePayload(mailwrite.SendOperationKey), "to@example.test", `to@example.test\r\nBcc: hidden@example.test`, 1)},
		"mail_duplicate_recipient": {mailwrite.SendOperationKey, strings.Replace(writePayload(mailwrite.SendOperationKey), "bcc@example.test", "TO@example.test", 1)},
		"mail_missing_bcc":         {mailwrite.SendOperationKey, strings.Replace(writePayload(mailwrite.SendOperationKey), `"bcc":[{"address":"bcc@example.test"}],`, "", 1)},
		"mail_from":                {mailwrite.SendOperationKey, strings.Replace(writePayload(mailwrite.SendOperationKey), `"subject":`, `"from":"hidden@example.test","subject":`, 1)},
		"reply_missing_original":   {mailwrite.ReplyOperationKey, `{"message":` + outgoingMessage + `}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAccountWriteFixture()
			s := f.selection(t)
			r := writeRequest(t, tc.key, tc.payload)
			out, err := s.InvokeConversationTool(t.Context(), r)
			if err == nil && out.Status != "failed" || len(f.writes) != 0 || len(f.lookups) != 0 || f.verifyCalls != 0 {
				t.Fatalf("invalid content approved or sent: %+v %v verification=%d", out, err, f.verifyCalls)
			}
		})
	}
	for _, field := range []string{"access", "confirmation", "idempotency_key", "request_id", "secret"} {
		t.Run("outer_"+field, func(t *testing.T) {
			f := newAccountWriteFixture()
			s := f.selection(t)
			r := writeRequest(t, mailwrite.SendOperationKey, writePayload(mailwrite.SendOperationKey))
			r.Call.Arguments = strings.Replace(r.Call.Arguments, `{"account_key":`, `{"`+field+`":"injected","account_key":`, 1)
			out, err := s.InvokeConversationTool(t.Context(), r)
			if err == nil && out.Status != "failed" || len(f.writes) != 0 {
				t.Fatal("model supplied owner control")
			}
		})
	}
}

func TestAccountWriteUnknownNeverResendsAndRejectsForeignReceipts(t *testing.T) {
	cases := map[string]func(*integration.ConnectionAccountWriteResult){
		"not_found": func(r *integration.ConnectionAccountWriteResult) {
			r.Status = integration.AccountWriteNotFound
			r.Receipt = nil
		},
		"uncertain": func(r *integration.ConnectionAccountWriteResult) {
			r.Status = integration.AccountWriteUncertain
			r.Receipt = nil
		},
		"foreign_source": func(r *integration.ConnectionAccountWriteResult) { r.Source.ConnectionKey = "other" },
		"foreign_ref": func(r *integration.ConnectionAccountWriteResult) {
			r.Receipt = json.RawMessage(strings.Replace(string(r.Receipt), "owner-invocation", "other-invocation", 1))
		},
		"invalid_time": func(r *integration.ConnectionAccountWriteResult) { r.RecordedAt = "today" },
		"private_response": func(r *integration.ConnectionAccountWriteResult) {
			r.Receipt = json.RawMessage(`{"secret":"must-not-leak"}`)
		},
		"invalid_delivery": func(r *integration.ConnectionAccountWriteResult) {
			r.Receipt = json.RawMessage(strings.Replace(string(r.Receipt), `"unknown"`, `"delivered"`, 1))
		},
	}
	for name, modify := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAccountWriteFixture()
			f.modifyResult = modify
			s := f.selection(t)
			r := writeRequest(t, mailwrite.SendOperationKey, writePayload(mailwrite.SendOperationKey))
			for n := 0; n < 3; n++ {
				var out sdk.Result
				var err error
				if n == 0 {
					out, err = s.InvokeConversationTool(t.Context(), r)
				} else {
					out, err = s.ReconcileConversationTool(t.Context(), r)
				}
				if err != nil || out.Status != "uncertain" || len(out.Content) != 0 {
					t.Fatalf("false known result %+v %v", out, err)
				}
			}
			if len(f.writes) != 1 || len(f.lookups) != 2 || !reflect.DeepEqual(f.writes[0], f.lookups[1]) {
				t.Fatal("unknown write resent or new identity used")
			}
		})
	}
	f := newAccountWriteFixture()
	f.status = integration.AccountWriteFailed
	s := f.selection(t)
	r := writeRequest(t, mailwrite.SendOperationKey, writePayload(mailwrite.SendOperationKey))
	if out, err := s.InvokeConversationTool(t.Context(), r); err != nil || out.Status != "failed" {
		t.Fatal(out, err)
	}
	if out, err := s.ReconcileConversationTool(t.Context(), r); err != nil || out.Status != "failed" || len(f.writes) != 1 {
		t.Fatal(out, err)
	}
}

func TestAccountWriteRechecksResponseAndHistoricalResult(t *testing.T) {
	for _, scenario := range []string{"grant", "revision", "tool"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAccountWriteFixture()
			s := f.selection(t)
			r := writeRequest(t, mailwrite.SendOperationKey, writePayload(mailwrite.SendOperationKey))
			f.afterWrite = func() {
				switch scenario {
				case "grant":
					f.blocked["account:mail_send"] = true
				case "revision":
					f.accounts[0].UpdatedAt = "new"
				case "tool":
					f.toolAllowed = false
				}
			}
			if out, err := s.InvokeConversationTool(t.Context(), r); err == nil || len(out.Content) > 0 || len(f.writes) != 1 {
				t.Fatalf("revoked reply leaked %+v %v", out, err)
			}
		})
	}
	f := newAccountWriteFixture()
	s := f.selection(t)
	r := writeRequest(t, mailwrite.SendOperationKey, writePayload(mailwrite.SendOperationKey))
	out, err := s.InvokeConversationTool(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.Confirmation = nil
	r.ConfirmationID = ""
	for _, change := range []func(*sdk.Result){func(v *sdk.Result) { v.Completion = "completed" }, func(v *sdk.Result) { v.ResourceID = "wrong" }, func(v *sdk.Result) {
		v.Content = json.RawMessage(strings.Replace(string(v.Content), `2026-09-12T10:00:00Z`, "invalid", -1))
	}} {
		v := out
		change(&v)
		if s.AuthorizeConversationToolResult(t.Context(), r, v) == nil {
			t.Fatal("invalid historical receipt exposed")
		}
	}
	// Changing content must retain the host identity so the owner can detect a
	// conflict. The caller cannot evade it by making Tools hash a new payload.
	r.Confirmation = &sdk.Confirmation{ID: "approval"}
	r.ConfirmationID = "approval"
	r.Call.Arguments = strings.Replace(r.Call.Arguments, "完整正文", "不同正文", 1)
	if _, err := s.InvokeConversationTool(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if f.writes[0].RequestID != f.writes[1].RequestID || reflect.DeepEqual(f.writes[0].Payload, f.writes[1].Payload) {
		t.Fatal("content affected owner idempotency identity")
	}
}

func TestAccountWriteDiscoveryAndAvailabilityDoNotPerformIO(t *testing.T) {
	f := newAccountWriteFixture()
	s := f.selection(t)
	c, m := f.adapters()
	for _, d := range writeDefinitions() {
		var ready bool
		var err error
		if strings.HasPrefix(d.Key, "calendar_") {
			ready, err = c.ConversationToolAvailable(t.Context(), writeRequest(t, d.Key, `{}`).Authority, d.Key)
		} else {
			ready, err = m.ConversationToolAvailable(t.Context(), writeRequest(t, d.Key, `{}`).Authority, d.Key)
		}
		if err != nil || !ready {
			t.Fatalf("unavailable %s %v", d.Key, err)
		}
	}
	for _, key := range []string{"calendar_write_accounts", "mail_write_accounts"} {
		r := writeRequest(t, key, `{}`)
		r.Call.Arguments = `{}`
		r.Confirmation = nil
		out, err := s.InvokeConversationTool(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out.Content), `"account_updated_at":"revision-1"`) {
			t.Fatalf("missing preview revision %s", out.Content)
		}
		f.listAccess = integration.ConnectionAccountAccess{}
		if s.AuthorizeConversationToolResult(t.Context(), r, out) == nil {
			t.Fatal("account disclosure survived revoked list permission")
		}
		f.listAccess = integration.ConnectionAccountAccess{Personal: true, Workspace: true}
	}
	if len(f.writes) != 0 || len(f.lookups) != 0 || len(f.requests) != 0 || f.verifyCalls != 0 {
		t.Fatal("catalog/discovery performed I/O or confirmation")
	}
	f.blocked["account:mail_send"] = true
	if ready, err := m.ConversationToolAvailable(t.Context(), writeRequest(t, mailwrite.SendOperationKey, `{}`).Authority, mailwrite.SendOperationKey); err != nil || ready {
		t.Fatal("write grant ignored", err)
	}
}

func TestCalendarWriteInspectionPreservesCompleteTarget(t *testing.T) {
	f := newAccountWriteFixture()
	s := f.selection(t)
	e := calendar.Event{ID: "exact-event", CalendarID: "primary", Title: "原活动", Status: "confirmed", Start: calendar.Moment{Date: "2026-11-01"}, End: calendar.Moment{Date: "2026-11-02"}}
	snapshot := calendarwrite.Snapshot{Event: e, Version: `"actual-etag"`, Kind: "single", Attendees: []calendarwrite.Attendee{{Address: "one@example.test", Kind: "optional"}}}
	f.set(snapshot)
	r := writeRequest(t, calendarwrite.InspectOperationKey, `{}`)
	r.Call.Arguments = `{"account_key":"account","calendar_id":"primary","event_id":"exact-event","time_zone":"America/New_York"}`
	r.Confirmation = nil
	out, err := s.InvokeConversationTool(t.Context(), r)
	if err != nil || out.Status != "completed" || len(f.requests) != 1 || f.requests[0].ContractSHA256 != calendarwrite.OperationSHA256(calendarwrite.InspectOperationKey) {
		t.Fatal(out, err)
	}
	var envelope calendarOutput
	_ = json.Unmarshal(out.Content, &envelope)
	var got calendarwrite.Snapshot
	_ = json.Unmarshal(envelope.Data, &got)
	if !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("inspection truncated or changed target: %+v", got)
	}
	snapshot.Attendees = nil
	f.set(snapshot)
	if out, err = s.InvokeConversationTool(t.Context(), r); err == nil && out.Status == "completed" {
		t.Fatal("incomplete attendees exposed as complete")
	}
}

func TestAccountWriteSelectionCoexistsWithReadFamilies(t *testing.T) {
	f := newAccountWriteFixture()
	reg := module.NewRegistry()
	c, m := f.adapters()
	for _, register := range []func(*module.Registry) error{c.Register, m.Register, (&module.CalendarAdapter{Accounts: f, Reads: f, Subject: f.subject, Authorize: f.authorize}).Register, (&module.MailAdapter{Accounts: f, Reads: f, Subject: f.subject, Authorize: f.authorize}).Register, (&module.WebAdapter{Accounts: f, Reads: f, Subject: f.subject, Authorize: f.authorize}).Register} {
		if err := register(reg); err != nil {
			t.Fatal(err)
		}
	}
	defs := append(writeDefinitions(), module.CalendarDefinitions()...)
	defs = append(defs, module.MailDefinitions()...)
	defs = append(defs, module.WebDefinitions()...)
	keys := []string{}
	for _, d := range defs {
		keys = append(keys, d.Key)
	}
	s, err := reg.Select(keys)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ConversationTools(t.Context(), writeRequest(t, mailwrite.SendOperationKey, `{}`).Authority)
	if err != nil || len(got) != 18 || len(module.CalendarDefinitions()) != 5 || len(module.MailDefinitions()) != 4 || len(module.WebDefinitions()) != 2 {
		t.Fatalf("read catalog changed: %d %v", len(got), err)
	}
}

func TestAccountWriteDiscoveryPaginationPinsOperationAndRevision(t *testing.T) {
	f := newAccountWriteFixture()
	second := f.accounts[0]
	second.Key = "second"
	second.Name = "第二个账号"
	f.accounts = append(f.accounts, second)
	f.blocked["account:mail_send"] = true
	s := f.selection(t)
	r := writeRequest(t, "mail_write_accounts", `{}`)
	r.Call.Arguments = `{"operation":"mail_send","limit":1}`
	out, err := s.InvokeConversationTool(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data struct {
			Items    []json.RawMessage `json:"items"`
			Complete bool              `json:"complete"`
			Cursor   string            `json:"next_cursor"`
		} `json:"data"`
	}
	if json.Unmarshal(out.Content, &envelope) != nil || len(envelope.Data.Items) != 0 || envelope.Data.Complete || envelope.Data.Cursor == "" {
		t.Fatalf("denied scan confused with complete list: %s", out.Content)
	}
	page, _ := json.Marshal(map[string]any{"operation": "mail_send", "limit": 1, "cursor": envelope.Data.Cursor})
	r.Call.Arguments = string(page)
	out, err = s.InvokeConversationTool(t.Context(), r)
	if err != nil || !strings.Contains(string(out.Content), `"key":"second"`) || !strings.Contains(string(out.Content), `"complete":true`) {
		t.Fatalf("next page %s %v", out.Content, err)
	}
	r.Call.Arguments = strings.Replace(string(page), "mail_send", "mail_reply", 1)
	if _, err = s.InvokeConversationTool(t.Context(), r); err == nil {
		t.Fatal("cursor changed write operation")
	}
	r.Call.Arguments = string(page)
	f.accounts[1].UpdatedAt = "changed"
	if _, err = s.InvokeConversationTool(t.Context(), r); err == nil {
		t.Fatal("cursor survived connection revision change")
	}
	if len(f.writes)+len(f.lookups)+len(f.requests) != 0 {
		t.Fatal("discovery performed an account operation")
	}
}
