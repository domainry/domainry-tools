package preferences

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/domainry/domainry-orm/driver"
	"github.com/domainry/domainry-orm/migration"
	"github.com/domainry/domainry-orm/query"
	"github.com/domainry/domainry-orm/schema"
	"github.com/domainry/domainry-orm/sqlhost"
	sdk "github.com/domainry/domainry-tools-sdk"
)

const table = "_tools_user_preferences"

type Store struct {
	db       sqlhost.Database
	renderer query.Renderer
	profile  driver.Profile
}

func New(db sqlhost.Database, renderer query.Renderer, profile driver.Profile) (*Store, error) {
	if db == nil || renderer == nil || profile == nil {
		return nil, fmt.Errorf("Tools preferences require host persistence and driver")
	}
	return &Store{db, renderer, profile}, nil
}
func Migrations(renderer query.Renderer) ([]migration.Migration, error) {
	statement, _, err := schema.NewTable(renderer, table).IfNotExists().Columns(
		schema.Column("owner_key", schema.TextKey(64)).NotNull(), schema.Column("tool_key", schema.TextKey(128)).NotNull(),
		schema.Column("enabled", schema.Boolean()).NotNull(), schema.Column("revision", schema.BigInt()).NotNull(), schema.Column("updated_at", schema.TextKey(40)).NotNull(),
	).PrimaryKey("owner_key", "tool_key").Build()
	if err != nil {
		return nil, err
	}
	return []migration.Migration{{Version: 1, Name: "tools_user_preferences", Statements: []string{statement}}}, nil
}
func owner(a sdk.Authority) (string, error) {
	if !a.Known || strings.TrimSpace(a.RuntimeID) == "" || strings.TrimSpace(a.WorkspaceID) == "" || strings.TrimSpace(a.UserID) == "" {
		return "", &sdk.Error{Class: "forbidden", Code: "tools.settings.subject_required"}
	}
	raw, _ := json.Marshal([]string{a.RuntimeID, a.WorkspaceID, a.UserID})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func scope(a sdk.Authority, key string) (query.Predicate, error) {
	own, err := owner(a)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) != key || key == "" || len(key) > 128 || strings.ContainsAny(key, "\x00\r\n") {
		return nil, &sdk.Error{Class: "bad_request", Code: "tools.settings.key_invalid"}
	}
	return query.And(query.Equal("owner_key", own), query.Equal("tool_key", key)), nil
}
func (s *Store) Preference(ctx context.Context, a sdk.Authority, key string) (sdk.Preference, error) {
	out := sdk.Preference{Key: key, Enabled: true}
	where, err := scope(a, key)
	if err != nil {
		return sdk.Preference{}, err
	}
	statement, args, err := query.NewSelectBuilder(s.renderer, table).Columns("enabled", "revision", "updated_at").Where(where).Build()
	if err != nil {
		return sdk.Preference{}, err
	}
	err = s.db.QueryRowContext(ctx, statement, args...).Scan(&out.Enabled, &out.Revision, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	return out, err
}
func (s *Store) SavePreference(ctx context.Context, a sdk.Authority, key string, in sdk.ToolSettingInput) (sdk.Preference, error) {
	where, err := scope(a, key)
	if err != nil {
		return sdk.Preference{}, err
	}
	if in.ExpectedRevision < 0 || in.ExpectedRevision == int64(^uint64(0)>>1) {
		return sdk.Preference{}, &sdk.Error{Class: "bad_request", Code: "tools.settings.revision_invalid"}
	}
	out := sdk.Preference{Key: key, Enabled: in.Enabled, Revision: in.ExpectedRevision + 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	var statement string
	var args []any
	if in.ExpectedRevision == 0 {
		own, _ := owner(a)
		statement, args, err = query.NewInsertBuilder(s.renderer, table).Columns("owner_key", "tool_key", "enabled", "revision", "updated_at").Values(own, key, out.Enabled, out.Revision, out.UpdatedAt).Build()
	} else {
		statement, args, err = query.NewUpdateBuilder(s.renderer, table).Set("enabled", out.Enabled).Set("revision", out.Revision).Set("updated_at", out.UpdatedAt).Where(query.And(where, query.Equal("revision", in.ExpectedRevision))).Build()
	}
	if err != nil {
		return sdk.Preference{}, err
	}
	result, err := s.db.ExecContext(ctx, statement, args...)
	if err != nil {
		if s.profile.ClassifyError(err) == driver.ErrorConflict {
			return sdk.Preference{}, changed()
		}
		return sdk.Preference{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return sdk.Preference{}, err
	}
	if count != 1 {
		return sdk.Preference{}, changed()
	}
	return out, nil
}
func changed() error { return &sdk.Error{Class: "conflict", Code: "tools.settings.revision_changed"} }
