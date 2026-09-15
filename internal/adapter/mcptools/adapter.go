package mcptools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/domainry/domainry-connector-sdk/mcptool"
	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools-sdk/schema"
	"github.com/domainry/domainry-tools/internal/adapter/accounttools"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

type SubjectResolver = accounttools.SubjectResolver
type Accounts = accounttools.Accounts

type Adapter struct {
	Accounts     Accounts
	Reads        integration.ConnectionAccountReads
	Writes       integration.ConnectionAccountWrites
	Subject      SubjectResolver
	Authorize    tools.Authorizer
	Confirmation sdk.ConfirmationVerifier
}

func Definitions() []sdk.Definition { return sdk.MCPDefinitions() }

func operation(toolKey string) string {
	switch toolKey {
	case sdk.MCPListToolsKey:
		return mcptool.ListToolsOperationKey
	case sdk.MCPCallToolKey:
		return mcptool.CallToolOperationKey
	}
	return ""
}

func (a *Adapter) read() *accounttools.Adapter {
	return &accounttools.Adapter{
		Ports: accounttools.Ports{Accounts: a.Accounts, Reads: a.Reads, Subject: a.Subject, Authorize: a.Authorize},
		Family: accounttools.Family{
			Name: "mcp", Definitions: Definitions()[:2], AccountsKey: sdk.MCPAccountsToolKey,
			DefaultOperation: sdk.MCPListToolsKey, Operations: []string{sdk.MCPListToolsKey},
			OperationKey: operation, OperationSHA256: mcptool.OperationSHA256,
			Prepare: a.prepareRead, IncludeAccountUpdatedAt: true,
		},
	}
}

func (a *Adapter) write() *accounttools.WriteAdapter {
	return &accounttools.WriteAdapter{
		WritePorts: accounttools.WritePorts{Accounts: a.Accounts, Writes: a.Writes, Subject: a.Subject, Authorize: a.Authorize, Confirmation: a.Confirmation},
		Family: accounttools.WriteFamily{
			Name: "mcp", AccountsKey: sdk.MCPAccountsToolKey, Definitions: Definitions()[2:],
			Operations: []string{sdk.MCPCallToolKey}, OperationKey: operation, OperationSHA256: mcptool.OperationSHA256,
			Prepare: a.prepareWrite, BeforeWrite: a.validateCurrentRemoteTool,
		},
	}
}

func (a *Adapter) Register(reg *tools.Registry) error {
	if a == nil || a.Reads == nil || a.Writes == nil {
		return fmt.Errorf("MCP tool adapter is incomplete")
	}
	if err := a.read().Register(reg); err != nil {
		return err
	}
	return a.write().Register(reg)
}

type accountArguments struct {
	AccountKey       string `json:"account_key"`
	AccountUpdatedAt string `json:"account_updated_at"`
}

type callArguments struct {
	AccountKey       string         `json:"account_key"`
	AccountUpdatedAt string         `json:"account_updated_at"`
	ToolName         string         `json:"tool_name"`
	Arguments        map[string]any `json:"arguments"`
}

func decode(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("MCP tool input has trailing data")
	}
	return nil
}

func validText(value string, limit int) bool {
	if value == "" || len(value) > limit || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (a *Adapter) prepareRead(_ string, raw []byte) (accounttools.Prepared, error) {
	var in accountArguments
	if decode(raw, &in) != nil || !validText(in.AccountKey, 1024) || !validText(in.AccountUpdatedAt, 128) {
		return accounttools.Prepared{}, fmt.Errorf("invalid MCP account selection")
	}
	return accounttools.Prepared{
		AccountKey: in.AccountKey, AccountUpdatedAt: in.AccountUpdatedAt, Payload: mcptool.ListToolsRequest{},
		Present: func(raw []byte) (any, error) { return presentCatalog(raw, in.AccountKey, in.AccountUpdatedAt) },
	}, nil
}

func (a *Adapter) prepareWrite(_ string, raw []byte) (accounttools.PreparedWrite, error) {
	var in callArguments
	if decode(raw, &in) != nil || !validText(in.AccountKey, 1024) || !validText(in.AccountUpdatedAt, 128) {
		return accounttools.PreparedWrite{}, fmt.Errorf("invalid MCP call selection")
	}
	request := mcptool.CallToolRequest{ToolName: in.ToolName, Arguments: in.Arguments, Approved: true}
	if err := request.Validate(); err != nil {
		return accounttools.PreparedWrite{}, err
	}
	return accounttools.PreparedWrite{
		AccountKey: in.AccountKey, AccountUpdatedAt: in.AccountUpdatedAt,
		Payload: request, Completion: "accepted",
		Present: func(raw []byte, _ string) (any, error) { return presentCallReceipt(raw, request.ToolName) },
	}, nil
}

type catalogTool struct {
	Name        string          `json:"name"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type catalogResult struct {
	AccountKey       string        `json:"account_key"`
	AccountUpdatedAt string        `json:"account_updated_at"`
	Tools            []catalogTool `json:"tools"`
	Complete         bool          `json:"complete"`
	Untrusted        bool          `json:"untrusted"`
}

func presentCatalog(raw []byte, accountKey, accountUpdatedAt string) (catalogResult, error) {
	if len(raw) == 0 || len(raw) > 4<<20 {
		return catalogResult{}, fmt.Errorf("MCP catalog size is invalid")
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return catalogResult{}, fmt.Errorf("MCP catalog is invalid")
	}
	var items []json.RawMessage
	if json.Unmarshal(root["tools"], &items) != nil || len(items) > 64 {
		return catalogResult{}, fmt.Errorf("MCP catalog tools are invalid")
	}
	complete := true
	if value := root["complete"]; len(value) > 0 && json.Unmarshal(value, &complete) != nil {
		return catalogResult{}, fmt.Errorf("MCP catalog completion is invalid")
	}
	if cursor := root["nextCursor"]; len(cursor) > 0 && string(cursor) != `""` && string(cursor) != "null" {
		complete = false
	}
	out := catalogResult{AccountKey: accountKey, AccountUpdatedAt: accountUpdatedAt, Tools: []catalogTool{}, Complete: complete, Untrusted: true}
	seen := map[string]bool{}
	for _, item := range items {
		var remote map[string]json.RawMessage
		if json.Unmarshal(item, &remote) != nil {
			return catalogResult{}, fmt.Errorf("MCP catalog entry is invalid")
		}
		var name string
		if json.Unmarshal(remote["name"], &name) != nil || !validText(name, 128) || seen[name] {
			return catalogResult{}, fmt.Errorf("MCP catalog tool name is invalid")
		}
		clean, err := sanitizeInputSchema(remote["inputSchema"])
		if err != nil {
			return catalogResult{}, fmt.Errorf("MCP catalog schema for %s is invalid: %w", name, err)
		}
		seen[name] = true
		out.Tools = append(out.Tools, catalogTool{Name: name, InputSchema: clean})
	}
	return out, nil
}

var schemaAnnotations = map[string]bool{
	"description": true, "title": true, "$comment": true, "examples": true,
	"default": true, "deprecated": true, "readOnly": true, "writeOnly": true,
}

func sanitizeInputSchema(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return nil, fmt.Errorf("schema size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, fmt.Errorf("schema JSON is invalid")
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema root must be an object")
	}
	cleaned, err := sanitizeSchemaValue(root, 0)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(cleaned)
	if err != nil || len(out) > 64<<10 {
		return nil, fmt.Errorf("sanitized schema is invalid")
	}
	compiled, err := schema.CompileSchema(out)
	if err != nil {
		return nil, err
	}
	// MCP arguments are always a JSON object. Reject a catalog schema that
	// would reject even an empty object only when it explicitly has no object
	// path; normal required properties remain valid.
	_ = compiled
	return out, nil
}

func sanitizeSchemaValue(value any, depth int) (any, error) {
	if depth > 32 {
		return nil, fmt.Errorf("schema is too deep")
	}
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > 256 {
			return nil, fmt.Errorf("schema object is too large")
		}
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if schemaAnnotations[key] {
				continue
			}
			if !utf8.ValidString(key) || len(key) > 256 || strings.ContainsRune(key, 0) {
				return nil, fmt.Errorf("schema key is invalid")
			}
			if key == "$ref" {
				ref, ok := child.(string)
				if !ok || !strings.HasPrefix(ref, "#/") || len(ref) > 2048 {
					return nil, fmt.Errorf("external schema references are forbidden")
				}
			}
			clean, err := sanitizeSchemaValue(child, depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = clean
		}
		return out, nil
	case []any:
		if len(typed) > 256 {
			return nil, fmt.Errorf("schema array is too large")
		}
		out := make([]any, len(typed))
		for i, child := range typed {
			clean, err := sanitizeSchemaValue(child, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = clean
		}
		return out, nil
	case string:
		if len(typed) > 8192 || !utf8.ValidString(typed) || strings.ContainsRune(typed, 0) {
			return nil, fmt.Errorf("schema string is invalid")
		}
	}
	return value, nil
}

func readSourceMatches(source integration.ConnectionAccountReadSource, subject integration.ConnectionAccountSubject, accountKey, accountUpdatedAt string) bool {
	return source.WorkspaceID == subject.WorkspaceID && source.ConnectionKey == accountKey && source.ConnectorKey == mcptool.ConnectorKey && source.ProviderKey == mcptool.ProviderKey && source.AccountUpdatedAt == accountUpdatedAt && source.Operation == mcptool.ListToolsOperationKey && source.ContractSHA256 == mcptool.ListToolsOperationSHA256
}

func (a *Adapter) validateCurrentRemoteTool(ctx context.Context, request sdk.Request, prepared accounttools.PreparedWrite) error {
	call, ok := prepared.Payload.(mcptool.CallToolRequest)
	if !ok {
		return &sdk.Error{Class: "bad_request", Code: "mcp.call_input_invalid"}
	}
	subject, err := a.Subject(ctx, request.Authority, integration.ActionIntegrationConnectionAccountsRead)
	if err != nil || subject.WorkspaceID != request.Authority.WorkspaceID || subject.UserID != request.Authority.UserID || !subject.Access.Personal && !subject.Access.Workspace {
		return &sdk.Error{Class: "forbidden", Code: "mcp.account_read_denied"}
	}
	op := integration.ConnectionAccountReadOperation{Operation: mcptool.ListToolsOperationKey, ContractSHA256: mcptool.ListToolsOperationSHA256}
	access, err := a.Reads.AuthorizeConnectionAccountRead(ctx, subject, prepared.AccountKey, op)
	if err != nil || !readSourceMatches(access.Source, subject, prepared.AccountKey, prepared.AccountUpdatedAt) {
		return &sdk.Error{Class: "forbidden", Code: "mcp.catalog_source_changed"}
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	payload, _ := json.Marshal(mcptool.ListToolsRequest{})
	result, err := a.Reads.ReadConnectionAccount(ctx, subject, prepared.AccountKey, integration.ConnectionAccountReadRequest{
		RequestID: "mcp-validate:" + hex.EncodeToString(nonce), Operation: op.Operation, ContractSHA256: op.ContractSHA256, Payload: payload,
	})
	if err != nil || !result.PayloadAvailable || result.Source != access.Source {
		return &sdk.Error{Class: "unavailable", Code: "mcp.catalog_read_failed"}
	}
	catalog, err := presentCatalog(result.Payload, prepared.AccountKey, prepared.AccountUpdatedAt)
	if err != nil || !catalog.Complete {
		return &sdk.Error{Class: "unavailable", Code: "mcp.catalog_invalid"}
	}
	var selected *catalogTool
	for i := range catalog.Tools {
		if catalog.Tools[i].Name == call.ToolName {
			selected = &catalog.Tools[i]
			break
		}
	}
	if selected == nil {
		return &sdk.Error{Class: "forbidden", Code: "mcp.tool_not_listed"}
	}
	compiled, err := schema.CompileSchema(selected.InputSchema)
	arguments, marshalErr := json.Marshal(call.Arguments)
	if err != nil || marshalErr != nil || schema.ValidateJSON(compiled, arguments) != nil {
		return &sdk.Error{Class: "bad_request", Code: "mcp.arguments_invalid"}
	}
	current, err := a.Reads.AuthorizeConnectionAccountRead(ctx, subject, prepared.AccountKey, op)
	if err != nil || current.Source != result.Source {
		return &sdk.Error{Class: "forbidden", Code: "mcp.catalog_source_changed"}
	}
	return nil
}

func presentCallReceipt(raw []byte, expectedTool string) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > 32<<10 {
		return nil, fmt.Errorf("MCP call receipt size is invalid")
	}
	var result map[string]json.RawMessage
	if decode(raw, &result) != nil {
		return nil, fmt.Errorf("MCP call receipt is invalid")
	}
	var name string
	if json.Unmarshal(result["tool_name"], &name) != nil || name != expectedTool {
		return nil, fmt.Errorf("MCP call receipt tool changed")
	}
	out := map[string]any{"tool_name": name}
	if content := result["content"]; len(content) > 0 {
		var blocks []any
		if json.Unmarshal(content, &blocks) != nil {
			return nil, fmt.Errorf("MCP call content is invalid")
		}
		out["content"] = blocks
	}
	if structured := result["structuredContent"]; len(structured) > 0 {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(structured))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			return nil, fmt.Errorf("MCP structured content is invalid")
		}
		out["structured_content"] = value
	}
	if len(out) == 1 {
		return nil, fmt.Errorf("MCP call receipt has no result")
	}
	return out, nil
}

func (a *Adapter) ConversationToolAvailable(ctx context.Context, authority sdk.Authority, key string) (bool, error) {
	switch key {
	case sdk.MCPAccountsToolKey, sdk.MCPListToolsKey:
		return a.read().ConversationToolAvailable(ctx, authority, key)
	case sdk.MCPCallToolKey:
		return a.write().ConversationToolAvailable(ctx, authority, key)
	}
	return false, nil
}

func (a *Adapter) ConversationToolResultReadAvailable(ctx context.Context, authority sdk.Authority, key string) (bool, error) {
	if key == sdk.MCPCallToolKey {
		return a.write().ConversationToolResultReadAvailable(ctx, authority, key)
	}
	if key == sdk.MCPAccountsToolKey || key == sdk.MCPListToolsKey {
		_, err := a.Subject(ctx, authority, integration.ActionIntegrationConnectionAccountsRead)
		return err == nil, err
	}
	return false, nil
}

var _ sdk.Availability = (*Adapter)(nil)
var _ sdk.ResultReadAvailability = (*Adapter)(nil)
