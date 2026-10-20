package preferences

import (
	"context"
	"fmt"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools-sdk/modulehost"
	application "github.com/domainry/domainry-tools/internal/application/preferences"
	persistence "github.com/domainry/domainry-tools/internal/infrastructure/persistence/database/preferences"
)

type Binding struct{ service *application.Service }

func (b *Binding) Settings() sdk.Settings         { return b.service }
func (b *Binding) Availability() sdk.Availability { return b.service }

// Open borrows the deployment's pool and submits only Tools-owned schema. The
// catalog must be the deployment/engine selection before preference filtering.
func Open(ctx context.Context, host modulehost.Persistence, catalog sdk.Catalog, connections sdk.ConnectionAvailability) (*Binding, error) {
	if host.Migrations == nil {
		return nil, fmt.Errorf("Tools preferences require the host migration ledger")
	}
	store, err := persistence.New(host.Database, host.Renderer, host.Profile)
	if err != nil {
		return nil, err
	}
	service, err := application.New(store, catalog, connections)
	if err != nil {
		return nil, err
	}
	migrations, err := persistence.Migrations(host.Renderer)
	if err != nil {
		return nil, err
	}
	if err = host.Migrations.ApplyOwnedMigrations(ctx, "tools", migrations); err != nil {
		return nil, err
	}
	return &Binding{service}, nil
}

var _ sdk.SettingsBinding = (*Binding)(nil)
