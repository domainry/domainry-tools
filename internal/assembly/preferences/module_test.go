package preferences_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/domainry/domainry-orm/dialect"
	"github.com/domainry/domainry-orm/migration"
	"github.com/domainry/domainry-orm/query"
	ormsqlite "github.com/domainry/domainry-orm/sqlite"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools-sdk/modulehost"
	"github.com/domainry/domainry-tools/module"
	_ "modernc.org/sqlite"
)

type fixtureCatalog struct{}

func (fixtureCatalog) ConversationTools(context.Context, sdk.Authority) ([]sdk.Definition, error) {
	return []sdk.Definition{{Key: "local", Version: "1", Description: "Local tool"}}, nil
}
func (fixtureCatalog) ToolConnectionAvailable(_ context.Context, _ sdk.Authority, key string) (bool, error) {
	return key == "local", nil
}

type fixtureLedger struct {
	db       *sql.DB
	renderer query.Renderer
	calls    int
}

func (h *fixtureLedger) ApplyOwnedMigrations(ctx context.Context, owner string, migrations []migration.Migration) error {
	if owner != "tools" {
		return errors.New("unexpected migration owner")
	}
	h.calls++
	runner, err := migration.NewRunner(h.db, h.renderer, migration.Options{})
	if err != nil {
		return err
	}
	return runner.Apply(ctx, migrations)
}

func TestPublicSettingsModulePersistsCASAndAuthorityAcrossReopen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tools.db")
	var db *sql.DB
	var binding sdk.SettingsBinding
	dialect, err := dialect.New(dialect.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	renderer := dialect.WithSchema("")
	ledger := &fixtureLedger{renderer: renderer}
	open := func() {
		t.Helper()
		var err error
		db, err = sql.Open("sqlite", file)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		ledger.db = db
		binding, err = module.OpenSettings(t.Context(), modulehost.Persistence{Database: db, Renderer: renderer, Profile: ormsqlite.NewProfile(), Migrations: ledger}, fixtureCatalog{}, fixtureCatalog{})
		if err != nil {
			t.Fatal(err)
		}
	}
	open()
	defer func() { db.Close() }()
	a := sdk.Authority{Known: true, RuntimeID: "app", WorkspaceID: "workspace", UserID: "alice", RoleKey: "member"}
	initial, err := binding.Settings().ListToolSettings(t.Context(), a)
	if err != nil || len(initial) != 1 || !initial[0].Enabled || initial[0].Revision != 0 {
		t.Fatal("default", initial, err)
	}
	var group sync.WaitGroup
	var won, conflicts atomic.Int32
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := binding.Settings().UpdateToolSetting(t.Context(), a, "local", sdk.ToolSettingInput{Enabled: false, ToolVersion: "1"})
			if err == nil {
				won.Add(1)
				return
			}
			var coded *sdk.Error
			if errors.As(err, &coded) && coded.Code == "tools.settings.revision_changed" {
				conflicts.Add(1)
			} else {
				t.Error("unexpected concurrent write error", err)
			}
		}()
	}
	group.Wait()
	if won.Load() != 1 || conflicts.Load() != 15 {
		t.Fatal("CAS accepted multiple writes", won.Load(), conflicts.Load())
	}
	other := a
	other.UserID = "bob"
	if ready, err := binding.Availability().ConversationToolAvailable(t.Context(), other, "local"); err != nil || !ready {
		t.Fatal("personal toggle leaked", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	open()
	a.RoleKey = "new-role"
	stored, err := binding.Settings().ListToolSettings(t.Context(), a)
	if err != nil || stored[0].Revision != 1 || stored[0].Enabled || stored[0].Available || stored[0].UpdatedAt == "" {
		t.Fatal("reopened setting changed", stored, err)
	}
	if _, err = binding.Settings().UpdateToolSetting(t.Context(), a, "local", sdk.ToolSettingInput{Enabled: true, ToolVersion: "1"}); err == nil {
		t.Fatal("lost/stale request silently overwrote durable state")
	}
	updated, err := binding.Settings().UpdateToolSetting(t.Context(), a, "local", sdk.ToolSettingInput{Enabled: true, ExpectedRevision: 1, ToolVersion: "1"})
	if err != nil || updated.Revision != 2 || !updated.Available {
		t.Fatal("explicit current revision update", updated, err)
	}
	statement, args, err := query.NewSelectBuilder(renderer, migration.DefaultLedgerTable).Columns("version").Build()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(t.Context(), statement, args...)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		count++
	}
	rows.Close()
	if count != 1 || ledger.calls != 2 {
		t.Fatal("migration not retained by sole host ledger", count, ledger.calls)
	}
	db.Close()
	if _, err = binding.Settings().ListToolSettings(t.Context(), a); err == nil {
		t.Fatal("failed persistence returned default available state")
	}
}

func TestPublicSettingsRejectsMissingOwnerPortsBeforeMigration(t *testing.T) {
	if _, err := module.OpenSettings(t.Context(), modulehost.Persistence{}, fixtureCatalog{}, fixtureCatalog{}); err == nil {
		t.Fatal("missing host persistence accepted")
	}
}
