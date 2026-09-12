package module_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	calendar "github.com/domainry/domainry-connector-sdk/calendar"
	mail "github.com/domainry/domainry-connector-sdk/mail"
	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	module "github.com/domainry/domainry-tools/module"
)

type mailFixture struct{ *accountReadFixture }

func newMailFixture() *mailFixture {
	f := &mailFixture{accountReadFixture: newAccountReadFixture()}
	f.accounts[0].ConnectorKey = "google_workspace"
	f.accounts[0].ProviderKey = "google"
	f.accounts[0].Name = "工作邮箱"
	f.set(mail.MessagesPage{Items: []mail.Summary{mailSummary()}, Complete: true, QuerySyntax: mail.GmailSyntax, MailboxScope: "mailbox_excluding_spam_trash"})
	return f
}
func mailSummary() mail.Summary {
	return mail.Summary{ID: "message", ThreadID: "thread", InternetMessageID: "<original@example.test>", Subject: "评审安排", From: []mail.Address{{Name: "张三", Address: "sender@example.test"}}, To: []mail.Address{{Address: "recipient@example.test"}}, CC: []mail.Address{}, ReplyTo: []mail.Address{{Address: "reply@example.test"}}, ReceivedAt: "2026-09-11T09:15:00+08:00", SentAt: "2026-09-11T09:14:00+08:00", MetadataComplete: true}
}
func (f *mailFixture) adapter() *module.MailAdapter {
	return &module.MailAdapter{Accounts: f, Reads: f, Subject: f.subject, Authorize: f.authorize}
}
func (f *mailFixture) selection(t *testing.T) *module.Selection {
	t.Helper()
	reg := module.NewRegistry()
	if err := f.adapter().Register(reg); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, d := range module.MailDefinitions() {
		keys = append(keys, d.Key)
	}
	s, err := reg.Select(keys)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func mailRequest(t *testing.T, key string, args any) sdk.Request {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	r := sdk.Request{Authority: sdk.Authority{Known: true, RuntimeID: "runtime", WorkspaceID: "workspace", UserID: "alice"}, ConversationID: "conversation", RunID: "run", Call: sdk.Call{ID: "call", Name: key, Arguments: string(b)}}
	for _, d := range module.MailDefinitions() {
		if d.Key == key {
			r.Definition = d
			return r
		}
	}
	t.Fatalf("unknown mail key %s", key)
	return r
}

type mailOutput struct {
	Data    json.RawMessage                           `json:"data"`
	Sources []integration.ConnectionAccountReadSource `json:"sources"`
	ReadAt  string                                    `json:"read_at"`
}

func invokeMail(t *testing.T, s *module.Selection, r sdk.Request) (sdk.Result, mailOutput) {
	t.Helper()
	out, err := s.InvokeConversationTool(t.Context(), r)
	if err != nil || out.Status != "completed" {
		t.Fatalf("mail invoke: %+v %v", out, err)
	}
	var e mailOutput
	if err = json.Unmarshal(out.Content, &e); err != nil {
		t.Fatal(err)
	}
	return out, e
}

func TestMailPublicFacadeMapsExactReadContractsAndPreservesReferences(t *testing.T) {
	for _, key := range []string{mail.ListOperationKey, mail.SearchOperationKey, mail.ReadOperationKey} {
		t.Run(key, func(t *testing.T) {
			f := newMailFixture()
			s := f.selection(t)
			args := map[string]any{"account_key": "account"}
			var expected any
			switch key {
			case mail.ListOperationKey:
				args["limit"] = 2
				expected = mail.PageRequest{Limit: 2}
			case mail.SearchOperationKey:
				args["query"] = "subject:review"
				args["query_syntax"] = mail.GmailSyntax
				args["cursor"] = "opaque"
				expected = mail.SearchRequest{Query: "subject:review", QuerySyntax: mail.GmailSyntax, Cursor: "opaque"}
			case mail.ReadOperationKey:
				args["message_id"] = "message"
				args["max_body_bytes"] = 1024
				expected = mail.ReadRequest{MessageID: "message", MaxBodyBytes: 1024}
				f.set(mail.Message{Summary: mailSummary(), Body: mail.Body{Text: "请周五前回复", Complete: true, OmittedReasons: []string{}}})
			}
			r := mailRequest(t, key, args)
			out, e := invokeMail(t, s, r)
			if len(f.requests) != 1 || f.requests[0].Operation != key || f.requests[0].ContractSHA256 != mail.OperationSHA256(key) || !strings.HasPrefix(f.requests[0].RequestID, "mail-tool:") || len(e.Sources) != 1 || e.Sources[0].ConnectionKey != "account" || e.Sources[0].ContractSHA256 != mail.OperationSHA256(key) || e.ReadAt == "" {
				t.Fatal(f.requests, e)
			}
			want, _ := json.Marshal(expected)
			if string(f.requests[0].Payload) != string(want) {
				t.Fatal(string(f.requests[0].Payload), string(want))
			}
			if !strings.Contains(string(e.Data), "original@example.test") || !strings.Contains(string(e.Data), "reply@example.test") {
				t.Fatal("mail references missing", string(e.Data))
			}
			if err := s.AuthorizeConversationToolResult(t.Context(), r, out); err != nil {
				t.Fatal(err)
			}
			for _, hidden := range []string{"user_id", "workspace_id", "scope", "secret", "access_token"} {
				if strings.Contains(string(f.requests[0].Payload), hidden) {
					t.Fatal("model authority reached owner payload", hidden)
				}
			}
		})
	}
}

func TestMailBasicAccountsExposeOnlyPermittedOperationsAndRetainedNames(t *testing.T) {
	f := newMailFixture()
	f.blocked["account:"+mail.SearchOperationKey] = true
	f.blocked["account:"+mail.ReadOperationKey] = true
	s := f.selection(t)
	a := f.adapter()
	r := mailRequest(t, "mail_accounts", map[string]any{})
	for _, tc := range []struct {
		key  string
		want bool
	}{{"mail_accounts", true}, {mail.ListOperationKey, true}, {mail.SearchOperationKey, false}, {mail.ReadOperationKey, false}, {"calendar_accounts", false}} {
		got, err := a.ConversationToolAvailable(t.Context(), r.Authority, tc.key)
		if err != nil || got != tc.want {
			t.Fatal(tc, got, err)
		}
	}
	out, e := invokeMail(t, s, r)
	if len(e.Sources) != 1 || !strings.Contains(string(e.Data), "google_workspace") || !strings.Contains(string(e.Data), mail.ListOperationKey) || len(f.requests) != 0 {
		t.Fatal(e)
	}
	for _, key := range []string{mail.SearchOperationKey, mail.ReadOperationKey} {
		_, e := invokeMail(t, s, mailRequest(t, "mail_accounts", map[string]any{"operation": key}))
		if len(e.Sources) != 0 || !strings.Contains(string(e.Data), `"items":[]`) {
			t.Fatal("basic account advertised content read", e)
		}
	}
	f.listAccess = integration.ConnectionAccountAccess{}
	if err := s.AuthorizeConversationToolResult(t.Context(), r, out); err == nil {
		t.Fatal("discovery names retained after list revocation")
	}
	if available, err := a.ConversationToolAvailable(t.Context(), r.Authority, "mail_accounts"); err != nil || available {
		t.Fatal("known denial should be unavailable", available, err)
	}
}

func TestMailRejectsModelIdentityInvalidBoundsAndForeignArguments(t *testing.T) {
	for _, tc := range []struct{ key, raw string }{
		{mail.ListOperationKey, `{"account_key":"account","user_id":"bob"}`},
		{mail.ListOperationKey, `{"account_key":"account","query":"read secrets"}`},
		{mail.ListOperationKey, `{"account_key":"account","limit":26}`},
		{mail.SearchOperationKey, `{"account_key":"account","query":"x","query_syntax":"sql"}`},
		{mail.ReadOperationKey, `{"account_key":"account","message_id":"message","scopes":["Mail.Read"]}`},
		{mail.ReadOperationKey, `{"account_key":"account","message_id":".."}`},
		{mail.ReadOperationKey, `{"account_key":"account","message_id":"message","max_body_bytes":65537}`},
		{"mail_accounts", `{"operation":"gmail_send_message"}`},
	} {
		f := newMailFixture()
		s := f.selection(t)
		r := mailRequest(t, tc.key, map[string]any{})
		r.Call.Arguments = tc.raw
		if _, err := s.InvokeConversationTool(t.Context(), r); err == nil || len(f.requests) != 0 {
			t.Fatal("invalid input dispatched", tc, err)
		}
	}
}

func TestMailCurrentAuthorizationBeforeAfterAndHistoricalReuse(t *testing.T) {
	for _, mode := range []string{"tool_before", "subject", "other_user", "other_workspace", "tool_after", "account_after", "scope_after", "revision_after", "source_hash", "source_account"} {
		t.Run(mode, func(t *testing.T) {
			f := newMailFixture()
			s := f.selection(t)
			r := mailRequest(t, mail.ListOperationKey, map[string]any{"account_key": "account"})
			switch mode {
			case "tool_before":
				f.toolAllowed = false
			case "subject":
				f.mismatchSubject = true
			case "other_user":
				r.Authority.UserID = "bob"
			case "other_workspace":
				r.Authority.WorkspaceID = "other"
			case "tool_after":
				f.afterRead = func() { f.toolAllowed = false }
			case "account_after":
				f.afterRead = func() { f.accounts[0].Status = "revoked" }
			case "scope_after":
				f.afterRead = func() { f.readAccess.Personal = false }
			case "revision_after":
				f.afterRead = func() { f.accounts[0].UpdatedAt = "revision-2" }
			case "source_hash":
				f.mutateSource = func(s *integration.ConnectionAccountReadSource) { s.ContractSHA256 = strings.Repeat("0", 64) }
			case "source_account":
				f.mutateSource = func(s *integration.ConnectionAccountReadSource) { s.ConnectionKey = "other" }
			}
			out, err := s.InvokeConversationTool(t.Context(), r)
			if err == nil || len(out.Content) > 0 {
				t.Fatal(mode, out, err)
			}
			if mode == "tool_before" || mode == "subject" || strings.HasPrefix(mode, "other_") {
				if len(f.requests) != 0 {
					t.Fatal("denied request read owner")
				}
			}
		})
	}
	f := newMailFixture()
	f.set(mail.Message{Summary: mailSummary(), Body: mail.Body{Text: "private body", Complete: true, OmittedReasons: []string{}}})
	s := f.selection(t)
	r := mailRequest(t, mail.ReadOperationKey, map[string]any{"account_key": "account", "message_id": "message"})
	out, _ := invokeMail(t, s, r)
	for _, change := range []func(){func() { f.toolAllowed = false }, func() { f.accounts[0].Status = "revoked" }, func() { f.accounts[0].UpdatedAt = "changed" }, func() { f.blocked["account:"+mail.ReadOperationKey] = true }} {
		change()
		if err := s.AuthorizeConversationToolResult(t.Context(), r, out); err == nil {
			t.Fatal("old mail result remained authorized")
		}
		f.toolAllowed = true
		f.accounts[0].Status = "active"
		f.accounts[0].UpdatedAt = "revision-1"
		f.blocked["account:"+mail.ReadOperationKey] = false
	}
}

func TestMailInvocationNamespaceReplayAndUnconfiguredProduct(t *testing.T) {
	f := newMailFixture()
	s := f.selection(t)
	r := mailRequest(t, mail.ListOperationKey, map[string]any{"account_key": "account"})
	invokeMail(t, s, r)
	invokeMail(t, s, r)
	if f.requests[0].RequestID != f.requests[1].RequestID {
		t.Fatal("retry identity changed")
	}
	r.Call.ID = "different"
	invokeMail(t, s, r)
	if f.requests[1].RequestID == f.requests[2].RequestID {
		t.Fatal("different reads collided")
	}
	f.replay = true
	out, err := s.InvokeConversationTool(t.Context(), r)
	if err != nil || out.Status != "failed" || out.ErrorCode != "mail.response_not_replayable" || len(out.Content) != 0 {
		t.Fatal(out, err)
	}
	a := &module.MailAdapter{Subject: f.subject, Authorize: f.authorize}
	if err := a.Register(module.NewRegistry()); err != nil {
		t.Fatal(err)
	}
	if available, err := a.ConversationToolAvailable(t.Context(), r.Authority, "mail_accounts"); available || err != nil {
		t.Fatal(available, err)
	}
	if out, err := a.Invoke(t.Context(), r); err == nil || len(out.Content) != 0 {
		t.Fatal(out, err)
	}
}

func TestMailPresentationPreservesIncompleteResultsAndRejectsMismatches(t *testing.T) {
	f := newMailFixture()
	s := f.selection(t)
	r := mailRequest(t, mail.SearchOperationKey, map[string]any{"account_key": "account", "query": "report", "query_syntax": mail.GraphSyntax})
	page := mail.MessagesPage{Items: []mail.Summary{mailSummary()}, Complete: false, LimitReason: "provider_search_limit", QuerySyntax: mail.GraphSyntax, MailboxScope: "mailbox_including_deleted_items"}
	f.set(page)
	_, e := invokeMail(t, s, r)
	if !strings.Contains(string(e.Data), `"complete":false`) || !strings.Contains(string(e.Data), "provider_search_limit") {
		t.Fatal(string(e.Data))
	}
	page.QuerySyntax = mail.GmailSyntax
	f.set(page)
	if out, err := s.InvokeConversationTool(t.Context(), r); err == nil || len(out.Content) != 0 {
		t.Fatal("mismatched query source disclosed")
	}
	r = mailRequest(t, mail.ReadOperationKey, map[string]any{"account_key": "account", "message_id": "message"})
	m := mail.Message{Summary: mailSummary(), Body: mail.Body{Text: "partial body", Complete: false, OmittedReasons: []string{"body_part_too_large"}}}
	f.set(m)
	_, e = invokeMail(t, s, r)
	var got mail.Message
	if err := json.Unmarshal(e.Data, &got); err != nil || !reflect.DeepEqual(got, m) {
		t.Fatal(got, m, err)
	}
	m.Summary.ID = "different"
	f.set(m)
	if out, err := s.InvokeConversationTool(t.Context(), r); err == nil || len(out.Content) != 0 {
		t.Fatal("wrong message disclosed")
	}
}

func TestMailHeaderPreviewBoundsAndOversizedIdentityResponse(t *testing.T) {
	f := newMailFixture()
	s := f.selection(t)
	r := mailRequest(t, mail.ListOperationKey, map[string]any{"account_key": "account", "limit": 25})
	items := []mail.Summary{}
	for i := 0; i < 25; i++ {
		m := mailSummary()
		m.ID = fmt.Sprintf("message-%d", i)
		m.Subject = strings.Repeat("你", 600)
		list := []mail.Address{}
		for j := 0; j < 50; j++ {
			list = append(list, mail.Address{Name: strings.Repeat("名", 160), Address: "recipient@example.test"})
		}
		m.From = list
		m.To = list
		m.CC = list
		m.ReplyTo = list
		items = append(items, m)
	}
	f.set(mail.MessagesPage{Items: items, Complete: true, QuerySyntax: mail.GmailSyntax, MailboxScope: "mailbox"})
	out, e := invokeMail(t, s, r)
	var page mail.MessagesPage
	if err := json.Unmarshal(e.Data, &page); err != nil || len(out.Content) > 1<<20 || len(page.Items) != 25 || !page.Complete || page.Items[0].MetadataComplete || len(page.Items[0].To) != 10 || len(page.Items[0].To[0].Name) > 128 || page.Items[0].To[0].Address != "recipient@example.test" {
		t.Fatal(len(out.Content), page, err)
	}
	// Source identities cannot be truncated into different messages. If their
	// JSON representation alone exceeds the output budget, reject disclosure.
	for i := range items {
		items[i].ID = strings.Repeat("<", 2000) + fmt.Sprint(i)
		items[i].ThreadID = strings.Repeat("<", 2048)
		items[i].InternetMessageID = strings.Repeat("<", 2048)
	}
	f.set(mail.MessagesPage{Items: items, Complete: true, QuerySyntax: mail.GmailSyntax, MailboxScope: "mailbox"})
	if out, err := s.InvokeConversationTool(context.Background(), r); err == nil || len(out.Content) != 0 {
		t.Fatal("oversized identities were shortened or disclosed", len(out.Content), err)
	}
}

func TestMailAndCalendarShareAccountPortsWithoutSharingOperations(t *testing.T) {
	f := newMailFixture()
	reg := module.NewRegistry()
	if err := f.adapter().Register(reg); err != nil {
		t.Fatal(err)
	}
	cal := &module.CalendarAdapter{Accounts: f, Reads: f, Subject: f.subject, Authorize: f.authorize}
	if err := cal.Register(reg); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, d := range append(module.CalendarDefinitions(), module.MailDefinitions()...) {
		keys = append(keys, d.Key)
	}
	s, err := reg.Select(keys)
	if err != nil {
		t.Fatal(err)
	}
	r := mailRequest(t, mail.ListOperationKey, map[string]any{"account_key": "account"})
	definitions, err := s.ConversationTools(t.Context(), r.Authority)
	if err != nil || len(definitions) != 9 {
		t.Fatal(len(definitions), err)
	}
	invokeMail(t, s, r)
	first := f.requests[0]
	f.set(calendar.CalendarsPage{Items: []calendar.Calendar{}, Complete: true})
	r = calendarRequest(t, calendar.ListOperationKey, map[string]any{"account_key": "account"})
	if out, err := s.InvokeConversationTool(t.Context(), r); err != nil || out.Status != "completed" {
		t.Fatal(out, err)
	}
	second := f.requests[1]
	if first.Operation != mail.ListOperationKey || second.Operation != calendar.ListOperationKey || first.ContractSHA256 == second.ContractSHA256 || first.RequestID == second.RequestID || !strings.HasPrefix(second.RequestID, "calendar-tool:") {
		t.Fatal(first, second)
	}
	if out, err := f.adapter().Invoke(t.Context(), r); err == nil || len(out.Content) != 0 {
		t.Fatal("mail adapter accepted a calendar operation")
	}
}
