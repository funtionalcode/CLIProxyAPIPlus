package helps

import (
	"context"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type apiRequestLoggingRoundTripper struct {
	ctx  context.Context
	cfg  *config.Config
	auth *cliproxyauth.Auth
	base http.RoundTripper
}

func (t apiRequestLoggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	RecordAPIHTTPRequest(t.ctx, t.cfg, t.auth, req)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func withAPIRequestLoggingHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, client *http.Client, timeout time.Duration) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	if cfg == nil || !cfg.RequestLog {
		if timeout <= 0 {
			return client
		}
		clone := *client
		clone.Timeout = timeout
		return &clone
	}
	clone := *client
	if timeout > 0 {
		clone.Timeout = timeout
	}
	base := clone.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = apiRequestLoggingRoundTripper{
		ctx:  ctx,
		cfg:  cfg,
		auth: auth,
		base: base,
	}
	return &clone
}
