package conversation

import (
	"fmt"

	toolsdk "github.com/domainry/domainry-tools-sdk"
	analysistools "github.com/domainry/domainry-tools/internal/adapter/analysistools"
	mcptools "github.com/domainry/domainry-tools/internal/adapter/mcptools"
	reporttools "github.com/domainry/domainry-tools/internal/adapter/reporttools"
	application "github.com/domainry/domainry-tools/internal/application/tool"
)

type factory struct{}

func NewFactory() toolsdk.ConversationToolFactory { return factory{} }

func (factory) ConversationToolDefinitions(capabilities toolsdk.ConversationToolCapabilities) []toolsdk.Definition {
	definitions := append(toolsdk.ReportQueryDefinitions(), toolsdk.AnalysisDefinitions()...)
	if capabilities.MCP {
		definitions = append(definitions, toolsdk.MCPDefinitions()...)
	}
	return definitions
}

func (f factory) AssembleConversationTools(input toolsdk.ConversationToolAssembly) (toolsdk.Host, error) {
	if input.Base == nil || input.ReportSource == nil || input.AnalysisSource == nil || input.Authorize == nil {
		return nil, fmt.Errorf("conversation tool assembly is incomplete")
	}
	registry := application.NewRegistry()
	report := &reporttools.Adapter{Source: input.ReportSource, Authorize: application.Authorizer(input.Authorize)}
	if err := report.Register(registry); err != nil {
		return nil, err
	}
	analysis := &analysistools.Adapter{Source: input.AnalysisSource, Authorize: application.Authorizer(input.Authorize)}
	if err := analysis.Register(registry); err != nil {
		return nil, err
	}
	if input.MCP != nil {
		if input.MCP.Accounts == nil || input.MCP.Reads == nil || input.MCP.Writes == nil || input.MCP.Subject == nil {
			return nil, fmt.Errorf("MCP conversation tool ports are incomplete")
		}
		confirmation, ok := input.Base.(toolsdk.ConfirmationVerifier)
		if !ok || confirmation == nil {
			return nil, &toolsdk.Error{Class: "unavailable", Code: "mcp.confirmation_verifier_unavailable"}
		}
		mcp := &mcptools.Adapter{
			Accounts:     input.MCP.Accounts,
			Reads:        input.MCP.Reads,
			Writes:       input.MCP.Writes,
			Subject:      mcptools.SubjectResolver(input.MCP.Subject),
			Authorize:    application.Authorizer(input.Authorize),
			Confirmation: confirmation,
		}
		if err := mcp.Register(registry); err != nil {
			return nil, err
		}
	}
	definitions := f.ConversationToolDefinitions(toolsdk.ConversationToolCapabilities{MCP: input.MCP != nil})
	keys := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		keys = append(keys, definition.Key)
	}
	selected, err := registry.Select(keys)
	if err != nil {
		return nil, err
	}
	return application.Combine(input.Base, selected, keys)
}
