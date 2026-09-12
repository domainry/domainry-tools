// Package preferences owns user tool choices, independent of any Agent engine.
package preferences

import (
	"context"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/domainry/domainry-tools-sdk"
)

type Repository interface {
	Preference(context.Context, sdk.Authority, string) (sdk.Preference, error)
	SavePreference(context.Context, sdk.Authority, string, sdk.ToolSettingInput) (sdk.Preference, error)
}

type Service struct {
	repository  Repository
	catalog     sdk.Catalog
	connections sdk.ConnectionAvailability
}

func New(repository Repository, catalog sdk.Catalog, connections sdk.ConnectionAvailability) (*Service, error) {
	if repository == nil || catalog == nil || connections == nil {
		return nil, fmt.Errorf("Tools preferences require persistence, a current authorized catalog and explicit connection policy")
	}
	return &Service{repository, catalog, connections}, nil
}
func authority(a sdk.Authority) error {
	if !a.Known || strings.TrimSpace(a.RuntimeID) == "" || strings.TrimSpace(a.WorkspaceID) == "" || strings.TrimSpace(a.UserID) == "" {
		return &sdk.Error{Class: "forbidden", Code: "tools.settings.subject_required"}
	}
	return nil
}
func (s *Service) definitions(ctx context.Context, a sdk.Authority) (map[string]sdk.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := authority(a); err != nil {
		return nil, err
	}
	values, err := s.catalog.ConversationTools(ctx, a)
	if err != nil {
		return nil, err
	}
	out := map[string]sdk.Definition{}
	for _, d := range values {
		if d.Key == "" || d.Version == "" || out[d.Key].Key != "" {
			return nil, fmt.Errorf("Tools preference catalog has an invalid or duplicate definition")
		}
		out[d.Key] = d
	}
	return out, nil
}
func (s *Service) setting(ctx context.Context, a sdk.Authority, d sdk.Definition, p sdk.Preference) (sdk.ToolSetting, error) {
	out := sdk.ToolSetting{Preference: p, Version: d.Version, Description: d.Description, State: "disabled"}
	if !p.Enabled {
		return out, nil
	}
	ready := true
	if s.connections != nil {
		var err error
		ready, err = s.connections.ToolConnectionAvailable(ctx, a, d.Key)
		if err != nil {
			if ctx.Err() != nil {
				return sdk.ToolSetting{}, ctx.Err()
			}
			out.State = "connection_unknown"
			return out, nil
		}
	}
	out.Available = ready
	out.State = "connection_unavailable"
	if ready {
		out.State = "available"
	}
	return out, nil
}
func (s *Service) ListToolSettings(ctx context.Context, a sdk.Authority) ([]sdk.ToolSetting, error) {
	defs, err := s.definitions(ctx, a)
	if err != nil {
		return nil, err
	}
	out := []sdk.ToolSetting{}
	for key, d := range defs {
		p, err := s.repository.Preference(ctx, a, key)
		if err != nil {
			return nil, err
		}
		value, err := s.setting(ctx, a, d, p)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
func (s *Service) UpdateToolSetting(ctx context.Context, a sdk.Authority, key string, in sdk.ToolSettingInput) (sdk.ToolSetting, error) {
	defs, err := s.definitions(ctx, a)
	if err != nil {
		return sdk.ToolSetting{}, err
	}
	d, ok := defs[key]
	if !ok {
		return sdk.ToolSetting{}, &sdk.Error{Class: "forbidden", Code: "tools.settings.tool_unavailable"}
	}
	if in.ExpectedRevision < 0 || in.ToolVersion != d.Version {
		return sdk.ToolSetting{}, &sdk.Error{Class: "conflict", Code: "tools.settings.definition_changed"}
	}
	p, err := s.repository.SavePreference(ctx, a, key, in)
	if err != nil {
		return sdk.ToolSetting{}, err
	}
	return s.setting(ctx, a, d, p)
}

// Engines call this after catalog and action authorization. Avoid recursively
// reading their catalog here: a settings filter must never depend on itself.
func (s *Service) ConversationToolAvailable(ctx context.Context, a sdk.Authority, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := authority(a); err != nil {
		return false, err
	}
	p, err := s.repository.Preference(ctx, a, key)
	if err != nil || !p.Enabled {
		return false, err
	}
	// The explicit connection policy recognizes local tools, and rejects unknown
	// mappings. It cannot grant tool permission or expand a deployment selection.
	return s.connections.ToolConnectionAvailable(ctx, a, key)
}

var _ sdk.Settings = (*Service)(nil)
var _ sdk.Availability = (*Service)(nil)
