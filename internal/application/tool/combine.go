package tools

import (
	"context"
	"fmt"
	sdk "github.com/domainry/domainry-tools-sdk"
)

// Combined routes explicitly registered keys to an extension; other keys stay
// with the base host. It does not rely on a user's current visible catalog.
type Combined struct {
	Base, Extension sdk.Host
	keys            map[string]bool
}

func Combine(base, extension sdk.Host, keys []string) (*Combined, error) {
	if base == nil || extension == nil {
		return nil, fmt.Errorf("both tool hosts are required")
	}
	m := map[string]bool{}
	for _, k := range keys {
		if k == "" || m[k] {
			return nil, fmt.Errorf("invalid extension key")
		}
		m[k] = true
	}
	return &Combined{Base: base, Extension: extension, keys: m}, nil
}
func (h *Combined) host(key string) sdk.Host {
	if h.keys[key] {
		return h.Extension
	}
	return h.Base
}
func (h *Combined) ConversationTools(ctx context.Context, a sdk.Authority) ([]sdk.Definition, error) {
	base, e := h.Base.ConversationTools(ctx, a)
	if e != nil {
		return nil, e
	}
	extra, e := h.Extension.ConversationTools(ctx, a)
	if e != nil {
		return nil, e
	}
	out := []sdk.Definition{}
	for _, d := range base {
		if !h.keys[d.Key] {
			out = append(out, d)
		}
	}
	for _, d := range extra {
		if !h.keys[d.Key] {
			return nil, fmt.Errorf("undeclared extension tool")
		}
		out = append(out, d)
	}
	return out, nil
}
func (h *Combined) AuthorizeConversationTool(ctx context.Context, in sdk.Request) (sdk.Authorization, error) {
	return h.host(in.Definition.Key).AuthorizeConversationTool(ctx, in)
}
func (h *Combined) InvokeConversationTool(ctx context.Context, in sdk.Request) (sdk.Result, error) {
	return h.host(in.Definition.Key).InvokeConversationTool(ctx, in)
}
func (h *Combined) ReconcileConversationTool(ctx context.Context, in sdk.Request) (sdk.Result, error) {
	return h.host(in.Definition.Key).ReconcileConversationTool(ctx, in)
}
func (h *Combined) AuthorizeConversationToolResult(ctx context.Context, in sdk.Request, r sdk.Result) error {
	if p, ok := h.host(in.Definition.Key).(sdk.ResultAuthorizer); ok {
		return p.AuthorizeConversationToolResult(ctx, in, r)
	}
	a, e := h.AuthorizeConversationTool(ctx, in)
	if e != nil {
		return e
	}
	if !a.Granted {
		return denied()
	}
	return nil
}
