package middleware

import (
	"context"
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/ir"
)

// ThinkingPreservation returns an IR request middleware that preserves
// thinking configuration across format translations.
//
// When translating between formats that use different thinking representations
// (e.g., Claude's budget_tokens vs OpenAI's reasoning_effort), this middleware
// ensures the original configuration is preserved in passthrough for formats
// that support it.
func ThinkingPreservation() translator.IRRequestMiddleware {
	return func(ctx context.Context, req *ir.IRRequest, from, to translator.Format, next translator.IRRequestHandler) (*ir.IRRequest, error) {
		if req.Thinking == nil {
			return next(ctx, req)
		}

		// Only store if the target format uses a different representation.
		if from == translator.FormatClaude && to == translator.FormatOpenAIResponse {
			// Claude uses budget_tokens, OpenAI Responses uses effort.
			// Preserve the budget_tokens value for potential restoration.
			if req.Thinking.BudgetTokens != nil {
				if _, exists := req.Passthrough["__original_budget_tokens"]; !exists {
					if v, err := json.Marshal(*req.Thinking.BudgetTokens); err == nil {
						if req.Passthrough == nil {
							req.Passthrough = make(map[string]json.RawMessage)
						}
						req.Passthrough["__original_budget_tokens"] = v
					}
				}
			}
		} else if from == translator.FormatOpenAIResponse && to == translator.FormatClaude {
			// OpenAI Responses uses effort, Claude uses budget_tokens.
			if req.Thinking.Effort != "" {
				if _, exists := req.Passthrough["__original_effort"]; !exists {
					if v, err := json.Marshal(req.Thinking.Effort); err == nil {
						if req.Passthrough == nil {
							req.Passthrough = make(map[string]json.RawMessage)
						}
						req.Passthrough["__original_effort"] = v
					}
				}
			}
		}

		return next(ctx, req)
	}
}
