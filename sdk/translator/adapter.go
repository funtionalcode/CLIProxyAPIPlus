package translator

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/ir"
)

// ParseFunc parses raw JSON into an IR request.
type ParseFunc func(rawJSON []byte) (*ir.IRRequest, error)

// SerializeFunc serializes an IR request to raw JSON.
type SerializeFunc func(irReq *ir.IRRequest) ([]byte, error)

// ResponseParseFunc parses raw JSON into an IR response.
type ResponseParseFunc func(rawJSON []byte) (*ir.IRResponse, error)

// ResponseSerializeFunc serializes an IR response to raw JSON.
type ResponseSerializeFunc func(irResp *ir.IRResponse) ([]byte, error)

// formatParsers maps formats to their parser/serializer functions.
var formatParsers = map[Format]struct {
	Parse     ParseFunc
	Serialize SerializeFunc
}{
	FormatClaude:         {Parse: ir.ParseClaudeRequest, Serialize: ir.SerializeClaudeRequest},
	FormatOpenAI:         {Parse: ir.ParseOpenAIChatRequest, Serialize: ir.SerializeOpenAIChatRequest},
	FormatOpenAIResponse: {Parse: ir.ParseOpenAIResponsesRequest, Serialize: ir.SerializeOpenAIResponsesRequest},
	FormatCodex:          {Parse: ir.ParseCodexRequest, Serialize: ir.SerializeCodexRequest},
}

// formatResponseParsers maps formats to their response parser/serializer functions.
var formatResponseParsers = map[Format]struct {
	Parse     ResponseParseFunc
	Serialize ResponseSerializeFunc
}{
	FormatCodex: {Parse: ir.ParseCodexResponse, Serialize: ir.SerializeCodexResponse},
}

// RegisterFormatParser registers parser/serializer functions for a format.
func RegisterFormatParser(f Format, parse ParseFunc, serialize SerializeFunc) {
	formatParsers[f] = struct {
		Parse     ParseFunc
		Serialize SerializeFunc
	}{Parse: parse, Serialize: serialize}
}

// RegisterFormatResponseParser registers response parser/serializer functions for a format.
func RegisterFormatResponseParser(f Format, parse ResponseParseFunc, serialize ResponseSerializeFunc) {
	formatResponseParsers[f] = struct {
		Parse     ResponseParseFunc
		Serialize ResponseSerializeFunc
	}{Parse: parse, Serialize: serialize}
}

// WrapIRRequestMiddleware converts an IRRequestMiddleware into a raw RequestMiddleware.
// The wrapper parses the request body into IR, calls the IR middleware, then serializes back.
func WrapIRRequestMiddleware(mw IRRequestMiddleware) RequestMiddleware {
	return func(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error) {
		fromParsers, fromOK := formatParsers[req.Format]
		if !fromOK {
			// No parser available for this format, pass through.
			return next(ctx, req)
		}

		// Store raw body in metadata so IR middleware (e.g. CompactPassthrough)
		// can access the original JSON before parsing.
		if req.Metadata == nil {
			req.Metadata = make(map[string]any)
		}
		req.Metadata["__raw_body"] = req.Body

		irReq, err := fromParsers.Parse(req.Body)
		if err != nil {
			// Parse failure — fall back to raw passthrough.
			return next(ctx, req)
		}

		// Carry metadata into IR.
		if irReq.Metadata == nil && req.Metadata != nil {
			irReq.Metadata = req.Metadata
		} else {
			for k, v := range req.Metadata {
				irReq.Metadata[k] = v
			}
		}

		// Determine target format from the next handler's perspective.
		// The pipeline will set req.Format to `to` after translation,
		// so we need to look ahead. We pass `to` as the destination
		// format encoded in the request metadata by the pipeline caller.
		to := Format("")
		if v, ok := req.Metadata["__to_format"]; ok {
			if s, ok := v.(string); ok {
				to = Format(s)
			}
		}

		terminal := func(ctx context.Context, ir *ir.IRRequest) (*ir.IRRequest, error) {
			return ir, nil
		}

		result, err := mw(ctx, irReq, req.Format, to, terminal)
		if err != nil {
			return next(ctx, req)
		}

		// Serialize back to the source format.
		body, err := fromParsers.Serialize(result)
		if err != nil {
			return next(ctx, req)
		}

		req.Body = body
		return next(ctx, req)
	}
}

// WrapIRResponseMiddleware converts an IRResponseMiddleware into a raw ResponseMiddleware.
func WrapIRResponseMiddleware(mw IRResponseMiddleware) ResponseMiddleware {
	return func(ctx context.Context, resp ResponseEnvelope, next ResponseHandler) (ResponseEnvelope, error) {
		// Response middleware runs after translation, so the body is in the target format.
		// We need the target format's response parser.
		respParsers, ok := formatResponseParsers[resp.Format]
		if !ok {
			return next(ctx, resp)
		}

		irResp, err := respParsers.Parse(resp.Body)
		if err != nil {
			return next(ctx, resp)
		}

		from := Format("")
		to := resp.Format

		terminal := func(ctx context.Context, ir *ir.IRResponse) (*ir.IRResponse, error) {
			return ir, nil
		}

		result, err := mw(ctx, irResp, from, to, terminal)
		if err != nil {
			return next(ctx, resp)
		}

		body, err := respParsers.Serialize(result)
		if err != nil {
			return next(ctx, resp)
		}

		resp.Body = body
		return next(ctx, resp)
	}
}

// WrapIRRequestMiddlewareWithFormats converts an IRRequestMiddleware into a raw RequestMiddleware
// with explicit source and target format overrides.
func WrapIRRequestMiddlewareWithFormats(mw IRRequestMiddleware, from, to Format) RequestMiddleware {
	return func(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error) {
		actualFrom := from
		if actualFrom == "" {
			actualFrom = req.Format
		}

		fromParsers, fromOK := formatParsers[actualFrom]
		if !fromOK {
			return next(ctx, req)
		}

		// Store raw body in metadata so IR middleware can access the original JSON.
		if req.Metadata == nil {
			req.Metadata = make(map[string]any)
		}
		req.Metadata["__raw_body"] = req.Body

		irReq, err := fromParsers.Parse(req.Body)
		if err != nil {
			return next(ctx, req)
		}

		if irReq.Metadata == nil && req.Metadata != nil {
			irReq.Metadata = req.Metadata
		} else {
			for k, v := range req.Metadata {
				irReq.Metadata[k] = v
			}
		}

		actualTo := to
		if actualTo == "" {
			if v, ok := req.Metadata["__to_format"]; ok {
				if s, ok := v.(string); ok {
					actualTo = Format(s)
				}
			}
		}

		terminal := func(ctx context.Context, ir *ir.IRRequest) (*ir.IRRequest, error) {
			return ir, nil
		}

		result, err := mw(ctx, irReq, actualFrom, actualTo, terminal)
		if err != nil {
			return next(ctx, req)
		}

		body, err := fromParsers.Serialize(result)
		if err != nil {
			return next(ctx, req)
		}

		req.Body = body
		return next(ctx, req)
	}
}

// getFormatParser returns the parser/serializer for a format, or an error.
func getFormatParser(f Format) (ParseFunc, SerializeFunc, error) {
	p, ok := formatParsers[f]
	if !ok {
		return nil, nil, fmt.Errorf("translator: no IR parser registered for format %q", f)
	}
	return p.Parse, p.Serialize, nil
}
