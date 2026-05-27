package translator

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/ir"
)

// IRRequestMiddleware operates on parsed IR requests, allowing structured
// inspection and modification before serialization to the target format.
type IRRequestMiddleware func(ctx context.Context, req *ir.IRRequest, from, to Format, next IRRequestHandler) (*ir.IRRequest, error)

// IRResponseMiddleware operates on parsed IR responses.
type IRResponseMiddleware func(ctx context.Context, resp *ir.IRResponse, from, to Format, next IRResponseHandler) (*ir.IRResponse, error)

// IRRequestHandler processes an IR request.
type IRRequestHandler func(ctx context.Context, req *ir.IRRequest) (*ir.IRRequest, error)

// IRResponseHandler processes an IR response.
type IRResponseHandler func(ctx context.Context, resp *ir.IRResponse) (*ir.IRResponse, error)
