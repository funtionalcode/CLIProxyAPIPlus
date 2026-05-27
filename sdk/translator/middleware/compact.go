package middleware

import (
	"context"
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/ir"
)

// CompactPassthrough returns an IR request middleware that preserves
// compact-specific fields (context_management, truncation) through translation.
//
// When a request has Metadata["compact"] == true, fields that would normally
// be dropped during translation are stored in the IR Passthrough map and
// restored during serialization.
func CompactPassthrough() translator.IRRequestMiddleware {
	return func(ctx context.Context, req *ir.IRRequest, from, to translator.Format, next translator.IRRequestHandler) (*ir.IRRequest, error) {
		isCompact, _ := req.Metadata["compact"].(bool)
		if !isCompact {
			return next(ctx, req)
		}

		// Ensure passthrough map exists.
		if req.Passthrough == nil {
			req.Passthrough = make(map[string]json.RawMessage)
		}

		// These fields are commonly stripped during translation but are
		// important for compact requests to preserve context quality.
		compactFields := []string{"context_management", "truncation"}

		// Check if the raw body (stored in metadata by the adapter) contains
		// these fields and preserve them in passthrough.
		if rawBody, ok := req.Metadata["__raw_body"].([]byte); ok {
			for _, field := range compactFields {
				if _, exists := req.Passthrough[field]; !exists {
					if v := extractJSONField(rawBody, field); v != nil {
						req.Passthrough[field] = v
					}
				}
			}
		}

		return next(ctx, req)
	}
}

// extractJSONField extracts a top-level field from raw JSON.
func extractJSONField(raw []byte, field string) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	return obj[field]
}
