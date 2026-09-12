package preferences

import (
	"context"
	"errors"
	"sync"
	"testing"

	sdk "github.com/domainry/domainry-tools-sdk"
)

type fixtureRepository struct {
	mu     sync.Mutex
	values map[[4]string]sdk.Preference
	writes int
}

func preferenceKey(a sdk.Authority, key string) [4]string {
	return [4]string{a.RuntimeID, a.WorkspaceID, a.UserID, key}
}
func (r *fixtureRepository) Preference(ctx context.Context, a sdk.Authority, key string) (sdk.Preference, error) {
	if err := ctx.Err(); err != nil {
		return sdk.Preference{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.values[preferenceKey(a, key)]; ok {
		return p, nil
	}
	return sdk.Preference{Key: key, Enabled: true}, nil
}
func (r *fixtureRepository) SavePreference(ctx context.Context, a sdk.Authority, key string, in sdk.ToolSettingInput) (sdk.Preference, error) {
	if err := ctx.Err(); err != nil {
		return sdk.Preference{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := preferenceKey(a, key)
	previous := r.values[k]
	if previous.Revision != in.ExpectedRevision {
		return sdk.Preference{}, &sdk.Error{Class: "conflict", Code: "tools.settings.revision_changed"}
	}
	p := sdk.Preference{Key: key, Enabled: in.Enabled, Revision: previous.Revision + 1}
	r.values[k] = p
	r.writes++
	return p, nil
}

type fixtureCatalog struct {
	allowed bool
	version string
	calls   int
}

func (c *fixtureCatalog) ConversationTools(context.Context, sdk.Authority) ([]sdk.Definition, error) {
	c.calls++
	if !c.allowed {
		return nil, nil
	}
	return []sdk.Definition{{Key: "local", Version: c.version, Description: "local fixture"}, {Key: "calendar", Version: "1"}}, nil
}

type connectionPolicy func(context.Context, sdk.Authority, string) (bool, error)

func (p connectionPolicy) ToolConnectionAvailable(ctx context.Context, a sdk.Authority, key string) (bool, error) {
	return p(ctx, a, key)
}

func TestPreferencesPreserveOwnerVersionAndCurrentCatalog(t *testing.T) {
	repo := &fixtureRepository{values: map[[4]string]sdk.Preference{}}
	catalog := &fixtureCatalog{allowed: true, version: "1"}
	connectionCalls := 0
	service, err := New(repo, catalog, connectionPolicy(func(_ context.Context, _ sdk.Authority, key string) (bool, error) {
		connectionCalls++
		return key == "local", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	a := sdk.Authority{Known: true, RuntimeID: "runtime", WorkspaceID: "workspace", UserID: "alice", RoleKey: "user"}
	settings, err := service.ListToolSettings(t.Context(), a)
	if err != nil || len(settings) != 2 || settings[0].State != "connection_unavailable" || !settings[1].Available {
		t.Fatal("defaults or connection policy ignored", settings, err)
	}
	if _, err := service.UpdateToolSetting(t.Context(), a, "unselected", sdk.ToolSettingInput{Enabled: true, ToolVersion: "1"}); err == nil || repo.writes != 0 {
		t.Fatal("preferences created for unavailable tool")
	}
	disabled, err := service.UpdateToolSetting(t.Context(), a, "local", sdk.ToolSettingInput{Enabled: false, ToolVersion: "1"})
	if err != nil || disabled.Available || disabled.Revision != 1 {
		t.Fatal("disable", disabled, err)
	}
	beforeCalls, beforeCatalog := connectionCalls, catalog.calls
	if ready, err := service.ConversationToolAvailable(t.Context(), a, "local"); err != nil || ready || beforeCalls != connectionCalls || beforeCatalog != catalog.calls {
		t.Fatal("disabled availability invoked upstream or recursed into catalog")
	}
	for _, field := range []string{"user", "workspace", "runtime"} {
		other := a
		switch field {
		case "user":
			other.UserID = "bob"
		case "workspace":
			other.WorkspaceID = "elsewhere"
		case "runtime":
			other.RuntimeID = "other-app"
		}
		if ready, err := service.ConversationToolAvailable(t.Context(), other, "local"); err != nil || !ready {
			t.Fatal("preference crossed authority", field, err)
		}
	}
	a.RoleKey = "new-role"
	if ready, _ := service.ConversationToolAvailable(t.Context(), a, "local"); ready {
		t.Fatal("role change reset personal preference")
	}
	catalog.version = "2"
	if _, err := service.UpdateToolSetting(t.Context(), a, "local", sdk.ToolSettingInput{Enabled: true, ExpectedRevision: 1, ToolVersion: "1"}); err == nil || repo.writes != 1 {
		t.Fatal("stale deployment version changed setting")
	}
	catalog.allowed = false
	settings, err = service.ListToolSettings(t.Context(), a)
	if err != nil || len(settings) != 0 {
		t.Fatal("revoked catalog leaked preference")
	}
	if _, err := service.UpdateToolSetting(t.Context(), a, "local", sdk.ToolSettingInput{Enabled: true, ExpectedRevision: 1, ToolVersion: "2"}); err == nil {
		t.Fatal("enable granted a missing tool")
	}
	catalog.allowed = true
	settings, err = service.ListToolSettings(t.Context(), a)
	if err != nil || settings[1].Enabled || settings[1].Version != "2" {
		t.Fatal("restoring permission or upgrading tool reset disabled preference")
	}
	if ready, _ := service.ConversationToolAvailable(t.Context(), a, "unknown"); ready {
		t.Fatal("unknown connection mapping allowed")
	}
}

func TestPreferencesFailClosedAndPropagateCancellation(t *testing.T) {
	repo := &fixtureRepository{values: map[[4]string]sdk.Preference{}}
	catalog := &fixtureCatalog{allowed: true, version: "1"}
	if _, err := New(repo, catalog, nil); err == nil {
		t.Fatal("implicit connection policy accepted")
	}
	marker := errors.New("connection authority unavailable")
	service, _ := New(repo, catalog, connectionPolicy(func(context.Context, sdk.Authority, string) (bool, error) { return false, marker }))
	a := sdk.Authority{Known: true, RuntimeID: "runtime", WorkspaceID: "workspace", UserID: "alice"}
	if ready, err := service.ConversationToolAvailable(t.Context(), a, "calendar"); ready || !errors.Is(err, marker) {
		t.Fatal("connection failure became available", err)
	}
	listed, err := service.ListToolSettings(t.Context(), a)
	if err != nil || len(listed) != 2 || listed[0].State != "connection_unknown" || listed[0].Available {
		t.Fatal("connection outage hid settings or reported ready", listed, err)
	}
	if _, err := service.ListToolSettings(t.Context(), sdk.Authority{}); err == nil {
		t.Fatal("anonymous preferences exposed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.UpdateToolSetting(ctx, a, "local", sdk.ToolSettingInput{Enabled: false, ToolVersion: "1"}); !errors.Is(err, context.Canceled) || repo.writes != 0 {
		t.Fatal("cancelled settings write executed", err)
	}
}
