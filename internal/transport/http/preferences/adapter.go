package preferences

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/domainry/domainry-foundation/httpresponse"
	"github.com/domainry/domainry-foundation/modulehttp"
	identity "github.com/domainry/domainry-identity-sdk"
	"github.com/domainry/domainry-identity-sdk/authorization/evaluator"
	sdk "github.com/domainry/domainry-tools-sdk"
	"io"
	"net/http"
	"strings"
	"time"
)

type adapter struct{ handler http.Handler }

func (*adapter) Owner() string              { return "tools" }
func (*adapter) Name() string               { return "user_preferences" }
func (*adapter) ContractVersion() string    { return modulehttp.ContractVersion }
func (*adapter) Routes() []modulehttp.Route { return sdk.ToolSettingsRoutes() }
func (a *adapter) Handler() http.Handler    { return a.handler }

// NewAdapter owns the Tools HTTP contract. The host authenticates Identity;
// this owner adapter evaluates exact action/data permission for the current user.
func NewAdapter(settings sdk.Settings, runtime string) (modulehttp.Adapter, error) {
	if settings == nil || strings.TrimSpace(runtime) == "" {
		return nil, fmt.Errorf("Tools HTTP requires settings and runtime identity")
	}
	mux := http.NewServeMux()
	for _, route := range sdk.ToolSettingsRoutes() {
		mux.HandleFunc(route.Pattern(), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
			current, ok := identity.RequestIdentityFromContext(r.Context())
			if !ok || !current.Principal.Known {
				failure(w, r, 401, "tools.settings.login_required")
				return
			}
			p := current.Principal
			if p.MustChangePassword || p.AccessBundle == nil || p.WorkspaceID == "" || p.UserID == "" {
				failure(w, r, 403, "tools.settings.access_denied")
				return
			}
			decision, err := evaluator.Evaluate(*p.AccessBundle, identity.AccessRequest{ObjectKey: "tools.preferences", Action: route.Action.OperationKey}, identity.ResourceFacts{"owner_user_id": p.UserID, "workspace_id": p.WorkspaceID}, time.Now().UTC())
			if err != nil || !decision.Allowed {
				failure(w, r, 403, "tools.settings.access_denied")
				return
			}
			authority := sdk.Authority{Known: true, RuntimeID: runtime, WorkspaceID: p.WorkspaceID, UserID: p.UserID, RoleKey: p.RoleKey}
			if r.Method == "GET" {
				items, err := settings.ListToolSettings(r.Context(), authority)
				respond(w, r, map[string]any{"items": items}, err)
				return
			}
			var in struct {
				Enabled  *bool  `json:"enabled"`
				Revision *int64 `json:"expected_revision"`
				Version  string `json:"tool_version"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&in) != nil || in.Enabled == nil || in.Revision == nil || in.Version == "" {
				failure(w, r, 400, "tools.settings.input_invalid")
				return
			}
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				failure(w, r, 400, "tools.settings.input_invalid")
				return
			}
			value, err := settings.UpdateToolSetting(r.Context(), authority, r.PathValue("toolKey"), sdk.ToolSettingInput{Enabled: *in.Enabled, ExpectedRevision: *in.Revision, ToolVersion: in.Version})
			respond(w, r, value, err)
		})
	}
	result := &adapter{mux}
	if err := modulehttp.ValidateAdapter(result); err != nil {
		return nil, err
	}
	return result, nil
}
func respond(w http.ResponseWriter, r *http.Request, value any, err error) {
	if err != nil {
		var coded *sdk.Error
		if errors.As(err, &coded) {
			status := map[sdk.ErrorClass]int{
				sdk.ErrorClassForbidden:  403,
				sdk.ErrorClassBadRequest: 400,
				sdk.ErrorClassConflict:   409,
				sdk.ErrorClassNotFound:   404,
			}[coded.Class]
			if status != 0 {
				failure(w, r, status, coded.Code)
				return
			}
		}
		failure(w, r, 503, "tools.settings.unavailable")
		return
	}
	httpresponse.WriteJSON(w, http.StatusOK, value)
}
func failure(w http.ResponseWriter, r *http.Request, status int, code string) {
	httpresponse.WriteError(w, r, httpresponse.Failure{Status: status, Code: code})
}
