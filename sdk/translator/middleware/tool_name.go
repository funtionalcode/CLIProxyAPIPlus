package middleware

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/ir"
)

// ToolNameNormalization returns an IR request middleware that shortens
// tool names exceeding 64 characters and stores the original names
// for restoration in responses.
//
// The middleware also normalizes tool call IDs to deterministic short
// forms, storing the mapping in metadata for response translation.
func ToolNameNormalization() translator.IRRequestMiddleware {
	return func(ctx context.Context, req *ir.IRRequest, from, to translator.Format, next translator.IRRequestHandler) (*ir.IRRequest, error) {
		if len(req.Tools) == 0 && len(req.Messages) == 0 {
			return next(ctx, req)
		}

		nameMap := make(map[string]string)
		idMap := make(map[string]string)

		// Normalize tool definitions.
		for i := range req.Tools {
			short, original := ir.ShortenToolName(req.Tools[i].Name)
			if short != original {
				nameMap[short] = original
				req.Tools[i].OriginalName = original
				req.Tools[i].Name = short
			}
		}

		// Normalize tool call IDs in messages.
		for i := range req.Messages {
			for j := range req.Messages[i].ToolCalls {
				tc := &req.Messages[i].ToolCalls[j]
				short := ir.NormalizeToolCallID(tc.ID, idMap)
				if short != tc.ID {
					idMap[short] = tc.ID
					tc.ID = short
				}
			}
		}

		// Store maps in metadata for response middleware.
		if req.Metadata == nil {
			req.Metadata = make(map[string]any)
		}
		if len(nameMap) > 0 {
			req.Metadata["__tool_name_map"] = nameMap
		}
		if len(idMap) > 0 {
			req.Metadata["__tool_id_map"] = idMap
		}

		return next(ctx, req)
	}
}

// ToolNameRestoration returns an IR response middleware that restores
// original tool names and IDs using the mappings stored by ToolNameNormalization.
func ToolNameRestoration() translator.IRResponseMiddleware {
	return func(ctx context.Context, resp *ir.IRResponse, from, to translator.Format, next translator.IRResponseHandler) (*ir.IRResponse, error) {
		// The name/id maps are passed through the context or response metadata.
		// Since IRResponse doesn't have Metadata, we rely on the pipeline
		// to pass these through a context key.
		nameMap, _ := respGetContextValue(ctx, ctxKeyToolNameMap).(map[string]string)
		idMap, _ := respGetContextValue(ctx, ctxKeyToolIDMap).(map[string]string)

		if len(nameMap) > 0 {
			for i := range resp.ToolCalls {
				if original, ok := nameMap[resp.ToolCalls[i].Name]; ok {
					resp.ToolCalls[i].Name = original
				}
			}
		}

		if len(idMap) > 0 {
			for i := range resp.ToolCalls {
				if original, ok := idMap[resp.ToolCalls[i].ID]; ok {
					resp.ToolCalls[i].ID = original
				}
			}
		}

		return next(ctx, resp)
	}
}

type contextKey string

const (
	ctxKeyToolNameMap contextKey = "__tool_name_map"
	ctxKeyToolIDMap   contextKey = "__tool_id_map"
)

func respGetContextValue(ctx context.Context, key contextKey) any {
	return ctx.Value(key)
}

// ContextWithToolMaps returns a context with tool name and ID maps for response restoration.
func ContextWithToolMaps(ctx context.Context, nameMap, idMap map[string]string) context.Context {
	if nameMap != nil {
		ctx = context.WithValue(ctx, ctxKeyToolNameMap, nameMap)
	}
	if idMap != nil {
		ctx = context.WithValue(ctx, ctxKeyToolIDMap, idMap)
	}
	return ctx
}
