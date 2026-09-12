package module_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/domainry/domainry-connector-sdk/web"
	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools/module"
)

func webFixture() *accountReadFixture {
	f := newAccountReadFixture()
	f.accounts[0].ConnectorKey = "web"
	f.accounts[0].ProviderKey = "llm_proxy"
	f.accounts[0].Scope = integration.ConnectionAccountScopeWorkspace
	f.accounts[0].OwnerUserID = ""
	f.set(web.SearchResult{SearchID: "search-1", Scope: "ranked_results", Items: []web.SearchItem{{URL: "https://example.com/news", Title: "今日公告", Excerpts: []string{"最新资料"}}}})
	return f
}
func webAdapter(f *accountReadFixture, key string) *module.WebAdapter {
	return &module.WebAdapter{Accounts: f, Reads: f, Subject: f.subject, Authorize: f.authorize, ConnectionKey: key}
}
func webSelection(t *testing.T, a *module.WebAdapter) *module.Selection {
	t.Helper()
	r := module.NewRegistry()
	if err := a.Register(r); err != nil {
		t.Fatal(err)
	}
	s, err := r.Select([]string{web.SearchOperationKey, web.FetchOperationKey})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func webRequest(t *testing.T, key string, args any) sdk.Request {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	r := sdk.Request{Authority: sdk.Authority{Known: true, RuntimeID: "runtime", WorkspaceID: "workspace", UserID: "alice"}, ConversationID: "conversation", RunID: "run", Call: sdk.Call{ID: "call", Name: key, Arguments: string(b)}}
	for _, d := range module.WebDefinitions() {
		if d.Key == key {
			r.Definition = d
			return r
		}
	}
	t.Fatal("web definition missing")
	return r
}

type webOutput struct {
	Data    json.RawMessage                           `json:"data"`
	Sources []integration.ConnectionAccountReadSource `json:"sources"`
	ReadAt  string                                    `json:"read_at"`
}

func invokeWeb(t *testing.T, s *module.Selection, r sdk.Request) (sdk.Result, webOutput) {
	t.Helper()
	out, err := s.InvokeConversationTool(t.Context(), r)
	if err != nil || out.Status != "completed" {
		t.Fatalf("web invoke %+v %v", out, err)
	}
	var result webOutput
	if json.Unmarshal(out.Content, &result) != nil {
		t.Fatal("invalid envelope")
	}
	return out, result
}

func TestWebPublicToolsUseFixedConnectionAndRetainSources(t *testing.T) {
	for _, key := range []string{web.SearchOperationKey, web.FetchOperationKey} {
		t.Run(key, func(t *testing.T) {
			f := webFixture()
			a := webAdapter(f, "account")
			s := webSelection(t, a)
			var input any = web.SearchRequest{Query: "今日消息", Limit: 2}
			if key == web.FetchOperationKey {
				input = web.FetchRequest{URL: "https://EXAMPLE.com/news#part", MaxContentBytes: 1024}
				f.set(web.Page{RequestedURL: "https://example.com/news", URL: "https://example.com/news", Title: "今日公告", Content: "正文", SourceCompleteness: "unknown", Warnings: []string{"来源可能未完整提取"}})
			}
			r := webRequest(t, key, input)
			out, envelope := invokeWeb(t, s, r)
			if len(f.requests) != 1 || f.requests[0].Operation != key || f.requests[0].ContractSHA256 != web.OperationSHA256(key) || !strings.HasPrefix(f.requests[0].RequestID, "web-tool:") || len(envelope.Sources) != 1 || envelope.Sources[0].ConnectionKey != "account" || envelope.ReadAt != "2026-09-11T10:00:00Z" {
				t.Fatal("identity/source lost", envelope, f.requests)
			}
			if len(f.readSubjects) != 1 || f.readSubjects[0].Access.Personal || !f.readSubjects[0].Access.Workspace {
				t.Fatal("web is not workspace-only")
			}
			for _, forbidden := range []string{"account_key", "connection_key", "api_token", "processor", "workspace_id", "user_id", "base_url"} {
				if strings.Contains(string(f.requests[0].Payload), forbidden) {
					t.Fatal("host configuration in model payload", forbidden)
				}
			}
			if !strings.Contains(string(envelope.Data), "https://example.com/news") {
				t.Fatal("source URL lost")
			}
			if key == web.SearchOperationKey && !strings.Contains(string(envelope.Data), `"query":"今日消息"`) {
				t.Fatal("query provenance lost")
			}
			if key == web.FetchOperationKey && (strings.Contains(string(f.requests[0].Payload), "#") || !strings.Contains(string(envelope.Data), `"source_completeness":"unknown"`)) {
				t.Fatal("page normalization/completeness lost")
			}
			if err := s.AuthorizeConversationToolResult(t.Context(), r, out); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWebAvailabilityNeverSubstitutesAnotherAccount(t *testing.T) {
	f := webFixture()
	a := webAdapter(f, "account")
	r := webRequest(t, web.SearchOperationKey, web.SearchRequest{Query: "news"})
	if available, err := a.ConversationToolAvailable(t.Context(), r.Authority, r.Definition.Key); err != nil || !available {
		t.Fatal("configured account unavailable", err)
	}
	other := f.accounts[0]
	other.Key = "other"
	f.accounts = append(f.accounts, other)
	f.accounts[0].Status = "revoked"
	if available, err := a.ConversationToolAvailable(t.Context(), r.Authority, r.Definition.Key); err != nil || available {
		t.Fatal("another account substituted", err)
	}
	f.accounts[0].Status = "active"
	for _, key := range []string{"", "mail_accounts", "web_accounts"} {
		if available, err := a.ConversationToolAvailable(t.Context(), r.Authority, key); err != nil || available {
			t.Fatal("undeclared operation available", key, err)
		}
	}
	a.ConnectionKey = ""
	s := webSelection(t, a)
	if available, err := a.ConversationToolAvailable(t.Context(), r.Authority, r.Definition.Key); err != nil || available {
		t.Fatal("missing key available", err)
	}
	if out, err := s.InvokeConversationTool(t.Context(), r); err == nil && out.Status == "completed" {
		t.Fatal("missing connection invoked")
	}
	a.ConnectionKey = "account"
	f.accounts[0].Scope = integration.ConnectionAccountScopePersonal
	f.accounts[0].OwnerUserID = "alice"
	if available, err := a.ConversationToolAvailable(t.Context(), r.Authority, r.Definition.Key); err != nil || available {
		t.Fatal("personal account advertised", err)
	}
	if _, err := a.Invoke(t.Context(), r); err == nil {
		t.Fatal("personal account invoked")
	}
	if len(f.requests) != 0 {
		t.Fatal("invalid availability dispatched")
	}
}

func TestWebSourceRevocationAndConfiguredReplacement(t *testing.T) {
	f := webFixture()
	a := webAdapter(f, "account")
	s := webSelection(t, a)
	r := webRequest(t, web.SearchOperationKey, web.SearchRequest{Query: "news"})
	out, _ := invokeWeb(t, s, r)
	for _, mutate := range []func(){func() { f.toolAllowed = false }, func() { f.readAccess.Workspace = false }, func() { f.accounts[0].Status = "revoked" }, func() { f.accounts[0].UpdatedAt = "revision-2" }, func() { f.accounts[0].WorkspaceID = "other" }} {
		mutate()
		if err := s.AuthorizeConversationToolResult(t.Context(), r, out); err == nil {
			t.Fatal("old source remained readable")
		}
		f.toolAllowed = true
		f.readAccess.Workspace = true
		f.accounts[0].Status = "active"
		f.accounts[0].UpdatedAt = "revision-1"
		f.accounts[0].WorkspaceID = "workspace"
	}
	other := f.accounts[0]
	other.Key = "replacement"
	f.accounts = append(f.accounts, other)
	replacement := webAdapter(f, "replacement")
	next := webSelection(t, replacement)
	if available, err := replacement.ConversationToolAvailable(t.Context(), r.Authority, r.Definition.Key); err != nil || !available {
		t.Fatal("new connection unavailable", err)
	}
	if err := next.AuthorizeConversationToolResult(t.Context(), r, out); err == nil {
		t.Fatal("replacement adopted old source")
	}
	if len(f.requests) != 1 {
		t.Fatal("source authorization did network I/O")
	}
}

func TestWebPostReadAuthorizationAndSensitiveReplay(t *testing.T) {
	for _, which := range []string{"tool", "subject", "revision", "source", "replay"} {
		t.Run(which, func(t *testing.T) {
			f := webFixture()
			s := webSelection(t, webAdapter(f, "account"))
			r := webRequest(t, web.SearchOperationKey, web.SearchRequest{Query: "news"})
			switch which {
			case "tool":
				f.afterRead = func() { f.toolAllowed = false }
			case "subject":
				f.afterRead = func() { f.mismatchSubject = true }
			case "revision":
				f.afterRead = func() { f.accounts[0].UpdatedAt = "new" }
			case "source":
				f.mutateSource = func(s *integration.ConnectionAccountReadSource) { s.ConnectionKey = "other" }
			case "replay":
				f.replay = true
			}
			out, err := s.InvokeConversationTool(t.Context(), r)
			if err == nil && out.Status == "completed" {
				t.Fatal("revoked/replayed body returned")
			}
			if strings.Contains(string(out.Content), "最新资料") {
				t.Fatal("body leaked")
			}
			if which == "replay" && (err != nil || out.ErrorCode != "web.response_not_replayable") {
				t.Fatal("sensitive replay result", out, err)
			}
		})
	}
}

func TestWebRejectsForeignParametersAndMalformedSources(t *testing.T) {
	for _, tc := range []struct{ key, raw string }{
		{web.SearchOperationKey, `{"query":"x","connection_key":"other"}`}, {web.SearchOperationKey, `{"query":"x","account_key":"other"}`},
		{web.SearchOperationKey, `{"query":"x","api_token":"model"}`}, {web.SearchOperationKey, `{"query":"x","processor":"pro"}`},
		{web.SearchOperationKey, `{"query":"x","limit":11}`}, {web.SearchOperationKey, `{"query":"x","max_excerpt_bytes":99}`},
		{web.FetchOperationKey, `{"url":"http://127.0.0.1/"}`}, {web.FetchOperationKey, `{"url":"https://u:p@example.com/"}`},
		{web.FetchOperationKey, `{"url":"https://example.com/","max_content_bytes":65537}`}, {web.FetchOperationKey, `{"url":"https://example.com/","cookies":"private"}`},
	} {
		f := webFixture()
		s := webSelection(t, webAdapter(f, "account"))
		r := webRequest(t, tc.key, map[string]any{})
		r.Call.Arguments = tc.raw
		if out, err := s.InvokeConversationTool(t.Context(), r); err == nil && out.Status == "completed" {
			t.Fatal("invalid arguments accepted", tc.raw)
		}
		if len(f.requests) != 0 {
			t.Fatal("invalid input dispatched")
		}
	}
	for _, body := range []string{`{"search_id":"x","items":[],"scope":"complete","truncated":false}`, `{"search_id":"x","items":[{"url":"http://127.0.0.1/","title":"x","excerpts":[]}],"scope":"ranked_results","truncated":false}`} {
		f := webFixture()
		f.payload = []byte(body)
		s := webSelection(t, webAdapter(f, "account"))
		r := webRequest(t, web.SearchOperationKey, web.SearchRequest{Query: "news"})
		if out, err := s.InvokeConversationTool(t.Context(), r); err == nil && out.Status == "completed" {
			t.Fatal("bad source accepted")
		}
	}
}

func TestWebRegistrationAndIndependentFamilies(t *testing.T) {
	f := webFixture()
	a := webAdapter(f, "account")
	reg := module.NewRegistry()
	if err := a.Register(reg); err != nil {
		t.Fatal(err)
	}
	if err := (&module.MailAdapter{Accounts: f, Reads: f, Subject: f.subject, Authorize: f.authorize}).Register(reg); err != nil {
		t.Fatal(err)
	}
	if err := (&module.CalendarAdapter{Accounts: f, Reads: f, Subject: f.subject, Authorize: f.authorize}).Register(reg); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, definitions := range [][]sdk.Definition{module.WebDefinitions(), module.MailDefinitions(), module.CalendarDefinitions()} {
		for _, d := range definitions {
			keys = append(keys, d.Key)
		}
	}
	if _, err := reg.Select(keys); err != nil || len(keys) != 11 {
		t.Fatal("family composition failed", err)
	}
	for _, mutate := range []func(*module.WebAdapter){func(a *module.WebAdapter) { a.ConnectionKey = " invalid" }, func(a *module.WebAdapter) { a.Subject = nil }, func(a *module.WebAdapter) { a.Reads = nil }} {
		broken := webAdapter(f, "account")
		mutate(broken)
		if err := broken.Register(module.NewRegistry()); err == nil {
			t.Fatal("bad registration")
		}
	}
	unconfigured := &module.WebAdapter{Subject: f.subject, Authorize: f.authorize}
	if err := unconfigured.Register(module.NewRegistry()); err != nil {
		t.Fatal(err)
	}
	r := webRequest(t, web.SearchOperationKey, web.SearchRequest{Query: "news"})
	r.Definition.Key = ""
	r.Call.Name = ""
	r.Call.Arguments = `{}`
	if _, err := a.Invoke(t.Context(), r); err == nil {
		t.Fatal("empty key entered an unregistered discovery operation")
	}
}
