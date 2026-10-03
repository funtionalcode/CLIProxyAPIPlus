package executor

import (
	"context"
	"sync/atomic"
)

type downstreamWebsocketContextKey struct{}
type codexHTTPUpstreamContextKey struct{}
type requireUpstreamWebsocketContextKey struct{}
type upstreamAttemptTrackerContextKey struct{}

type upstreamAttemptTracker struct {
	attempted atomic.Bool
}

// CodexWebsocketHTTPThreshold is a conservative transport cutoff, not a context limit.
// Large compaction requests can exceed the upstream websocket message limit.
const CodexWebsocketHTTPThreshold = 16 << 20

// WithCodexHTTPUpstream keeps downstream websocket semantics while selecting HTTP upstream.
func WithCodexHTTPUpstream(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, codexHTTPUpstreamContextKey{}, true)
}

// CodexHTTPUpstream reports whether the handler is retaining HTTP continuation state.
func CodexHTTPUpstream(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(codexHTTPUpstreamContextKey{}).(bool)
	return enabled
}

// WithDownstreamWebsocket marks the current request as coming from a downstream websocket connection.
func WithDownstreamWebsocket(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, downstreamWebsocketContextKey{}, true)
}

// DownstreamWebsocket reports whether the current request originates from a downstream websocket connection.
func DownstreamWebsocket(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw := ctx.Value(downstreamWebsocketContextKey{})
	enabled, ok := raw.(bool)
	return ok && enabled
}

// WithRequiredUpstreamWebsocket marks a request whose incremental context is valid only on the current upstream websocket.
func WithRequiredUpstreamWebsocket(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requireUpstreamWebsocketContextKey{}, true)
}

// RequiredUpstreamWebsocket reports whether falling back to an HTTP upstream would lose request context.
func RequiredUpstreamWebsocket(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw := ctx.Value(requireUpstreamWebsocketContextKey{})
	enabled, ok := raw.(bool)
	return ok && enabled
}

// WithUpstreamAttemptTracker installs a fresh tracker for one provider execution attempt.
func WithUpstreamAttemptTracker(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, upstreamAttemptTrackerContextKey{}, &upstreamAttemptTracker{})
}

// MarkUpstreamAttempt records that the provider execution reached an upstream transport boundary.
func MarkUpstreamAttempt(ctx context.Context) {
	if ctx == nil {
		return
	}
	tracker, ok := ctx.Value(upstreamAttemptTrackerContextKey{}).(*upstreamAttemptTracker)
	if !ok || tracker == nil {
		return
	}
	tracker.attempted.Store(true)
}

// UpstreamAttempted reports whether the tracked provider execution reached an upstream transport boundary.
func UpstreamAttempted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	tracker, ok := ctx.Value(upstreamAttemptTrackerContextKey{}).(*upstreamAttemptTracker)
	return ok && tracker != nil && tracker.attempted.Load()
}
