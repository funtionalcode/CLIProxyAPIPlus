package headerdefaults

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchOfficialStableVersions(t *testing.T) {
	responses := map[string]string{
		npmRegistry + "@anthropic-ai/claude-code/latest": `{"name":"@anthropic-ai/claude-code","version":"2.1.318"}`,
		npmRegistry + "@anthropic-ai/sdk/latest":         `{"name":"@anthropic-ai/sdk","version":"1.4.0"}`,
		npmRegistry + "@openai/codex/latest":             `{"name":"@openai/codex","version":"0.166.0"}`,
		nodeReleases:                                     `[{"version":"v27.0.0-alpha.0"},{"version":"v24.10.0"},{"version":"v26.9.0"},{"version":"v26.13.0"},{"version":"invalid"}]`,
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "" {
			t.Fatal("version metadata must not receive credentials")
		}
		body, ok := responses[req.URL.String()]
		if !ok {
			t.Fatalf("unexpected metadata URL %s", req.URL)
		}
		delete(responses, req.URL.String())
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	got, failures := Fetch(context.Background(), client, true, true)
	want := Versions{Claude: "2.1.318", Package: "1.4.0", Runtime: "v26.13.0", Codex: "0.166.0"}
	if got != want || len(failures) != 0 || len(responses) != 0 {
		t.Fatalf("Fetch = %+v, failures=%v, remaining=%d", got, failures, len(responses))
	}
}

func TestFetchPreservesFailedFieldsAndSkipsDisabledProviders(t *testing.T) {
	for _, invalid := range []string{
		`{"name":"@openai/codex","version":"0.167.0-alpha.1"}`,
		`{"name":"wrong-package","version":"0.167.0"}`,
		`{"name":"@openai/codex","version":"0.167.0\r\nx-secret: true"}`,
		`{"name":"@openai/codex","version":"01.167.0"}`,
		`<html>gateway failure</html>`,
	} {
		t.Run(invalid, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.String() != npmRegistry+"@openai/codex/latest" {
					t.Fatal("queried disabled Claude metadata")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(invalid))}, nil
			})}
			got, failures := Fetch(context.Background(), client, false, true)
			if got != (Versions{}) || len(failures) != 1 || calls != 1 {
				t.Fatalf("invalid metadata accepted: %+v, %v", got, failures)
			}
			_, _ = Fetch(context.Background(), client, false, false)
			if calls != 1 {
				t.Fatal("disabled updates performed network IO")
			}
		})
	}
}

func TestFetchPartialFailureAndCancellation(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == npmRegistry+"@openai/codex/latest" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"name":"@openai/codex","version":"0.166.0"}`))}, nil
		}
		return nil, fmt.Errorf("proxy://secret:password@host")
	})}
	got, failures := Fetch(context.Background(), client, true, true)
	if got != (Versions{Codex: "0.166.0"}) || len(failures) != 3 {
		t.Fatalf("partial failure: %+v, %v", got, failures)
	}
	if strings.Contains(fmt.Sprint(failures), "secret") {
		t.Fatal("transport credentials exposed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Context().Err() != context.Canceled {
			t.Fatal("lookup did not use service lifecycle context")
		}
		return nil, req.Context().Err()
	})
	_, failures = Fetch(ctx, client, false, true)
	if len(failures) != 1 {
		t.Fatal("canceled lookup succeeded")
	}
}

func TestUserAgentVersionKeepsPlatform(t *testing.T) {
	cases := []struct{ current, prefix, suffix, want string }{
		{"codex_cli_rs/0.146.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9", "codex_cli_rs/", "", "codex_cli_rs/3.2.1 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9"},
		{"claude-cli/2.1.280 (external, cli)", "claude-cli/", " (external, cli)", "claude-cli/3.2.1 (external, cli)"},
		{"", "claude-cli/", " (external, cli)", "claude-cli/3.2.1 (external, cli)"},
		{"custom", "codex_cli_rs/", "", "codex_cli_rs/3.2.1"},
	}
	for _, tc := range cases {
		if got := UserAgentVersion(tc.current, tc.prefix, "3.2.1", tc.suffix); got != tc.want {
			t.Errorf("UserAgentVersion(%q)=%q want %q", tc.current, got, tc.want)
		}
	}
}
