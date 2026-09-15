// Package module is the stable in-process facade for domainry-tools.
// Implementations are private; callers select and assemble capabilities explicitly.
package module

import (
	"context"
	"github.com/domainry/domainry-foundation/modulehttp"
	toolsdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools-sdk/modulehost"
	analysistools "github.com/domainry/domainry-tools/internal/adapter/analysistools"
	calendartools "github.com/domainry/domainry-tools/internal/adapter/calendartools"
	mailtools "github.com/domainry/domainry-tools/internal/adapter/mailtools"
	mcptools "github.com/domainry/domainry-tools/internal/adapter/mcptools"
	recordtools "github.com/domainry/domainry-tools/internal/adapter/recordtools"
	reporttools "github.com/domainry/domainry-tools/internal/adapter/reporttools"
	scheduletools "github.com/domainry/domainry-tools/internal/adapter/scheduletools"
	webtools "github.com/domainry/domainry-tools/internal/adapter/webtools"
	application "github.com/domainry/domainry-tools/internal/application/tool"
	preferences "github.com/domainry/domainry-tools/internal/assembly/preferences"
	preferenceshttp "github.com/domainry/domainry-tools/internal/transport/http/preferences"
)

func OpenSettings(ctx context.Context, host modulehost.Persistence, catalog toolsdk.Catalog, connections toolsdk.ConnectionAvailability) (toolsdk.SettingsBinding, error) {
	return preferences.Open(ctx, host, catalog, connections)
}

func SettingsHTTPAdapter(settings toolsdk.Settings, runtimeID string) (modulehttp.Adapter, error) {
	return preferenceshttp.NewAdapter(settings, runtimeID)
}

type Combined = application.Combined

func Combine(base, extension toolsdk.Host, keys []string) (*Combined, error) {
	return application.Combine(base, extension, keys)
}

type Handler = application.Handler
type Authorizer = application.Authorizer
type ResultAuthorizer = application.ResultAuthorizer
type Registration = application.Registration
type Registry = application.Registry

func NewRegistry() *Registry { return application.NewRegistry() }

type Selection = application.Selection
type RecordSpec = recordtools.Spec
type RecordAdapter = recordtools.Adapter

func RecordDefinitions(s RecordSpec) []toolsdk.Definition {
	return recordtools.Definitions(s)
}

type CalendarAdapter = calendartools.Adapter
type CalendarSubjectResolver = calendartools.SubjectResolver

func CalendarDefinitions() []toolsdk.Definition { return calendartools.Definitions() }

type MailAdapter = mailtools.Adapter
type MailSubjectResolver = mailtools.SubjectResolver

func MailDefinitions() []toolsdk.Definition { return mailtools.Definitions() }

type CalendarWriteAdapter = calendartools.WriteAdapter
type MailWriteAdapter = mailtools.WriteAdapter

func CalendarWriteDefinitions() []toolsdk.Definition { return calendartools.WriteDefinitions() }
func MailWriteDefinitions() []toolsdk.Definition     { return mailtools.WriteDefinitions() }

type MCPAdapter = mcptools.Adapter
type MCPSubjectResolver = mcptools.SubjectResolver

func MCPDefinitions() []toolsdk.Definition { return mcptools.Definitions() }

type WebAdapter = webtools.Adapter
type WebSubjectResolver = webtools.SubjectResolver

func WebDefinitions() []toolsdk.Definition { return webtools.Definitions() }

type ReportAdapter = reporttools.Adapter
type ReportSource = reporttools.Source

func ReportDefinitions() []toolsdk.Definition { return reporttools.Definitions() }

type AnalysisAdapter = analysistools.Adapter
type AnalysisSource = analysistools.Source

func AnalysisDefinitions() []toolsdk.Definition { return analysistools.Definitions() }

type ScheduleAdapter = scheduletools.Adapter

func ScheduleDefinitions() []toolsdk.Definition { return scheduletools.Definitions() }
