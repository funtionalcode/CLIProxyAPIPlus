package proxygateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/turnstate"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func fetchProxySource(ctx context.Context, client *http.Client, source string) ([]string, error) {
	sourceURL, errParse := url.Parse(strings.TrimSpace(source))
	if errParse != nil || sourceURL.Hostname() == "" || (sourceURL.Scheme != "http" && sourceURL.Scheme != "https") {
		return nil, fmt.Errorf("source URL must use HTTP or HTTPS and include a host")
	}
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL.String(), nil)
	if errRequest != nil {
		return nil, fmt.Errorf("invalid source request")
	}
	resp, errFetch := client.Do(req)
	if errFetch != nil {
		var urlErr *url.Error
		if errors.As(errFetch, &urlErr) {
			errFetch = urlErr.Err
		}
		return nil, fmt.Errorf("failed to fetch proxy source: %w", errFetch)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("proxy source returned HTTP %d", resp.StatusCode)
	}
	const maxSourceBytes = 1 << 20
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxSourceBytes+1))
	if errRead != nil {
		return nil, fmt.Errorf("failed to read proxy source: %w", errRead)
	}
	if len(body) > maxSourceBytes {
		return nil, fmt.Errorf("proxy source exceeds 1 MiB")
	}
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	proxies := make([]string, 0)
	seen := make(map[string]bool)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		normalized := turnstate.NormalizeProxyURL(line)
		setting, errProxy := proxyutil.Parse(normalized)
		if errProxy != nil || setting.Mode != proxyutil.ModeProxy || setting.URL.Hostname() == "" ||
			(setting.URL.Path != "" && setting.URL.Path != "/") || setting.URL.RawQuery != "" || setting.URL.Fragment != "" {
			continue
		}
		port := setting.URL.Port()
		if !strings.Contains(line, "://") && port == "" {
			continue
		}
		if port != "" {
			n, errPort := strconv.Atoi(port)
			if errPort != nil || n < 1 || n > 65535 {
				continue
			}
		}
		if !seen[normalized] {
			proxies = append(proxies, normalized)
			seen[normalized] = true
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return nil, fmt.Errorf("invalid proxy source response: %w", errScan)
	}
	if len(proxies) == 0 {
		return nil, fmt.Errorf("proxy source contains no usable proxy URLs; expected one proxy per line")
	}
	return proxies, nil
}

func (g *Gateway) TestProxySource(ctx context.Context, source string) TestResult {
	return testProxySource(ctx, g.poolMgr.httpClient, source, g.TestProxy)
}

func testProxySource(ctx context.Context, client *http.Client, source string, probe func(context.Context, string) (int64, error)) TestResult {
	start := time.Now()
	proxies, errFetch := fetchProxySource(ctx, client, source)
	result := TestResult{Stage: "source", SourceLatencyMs: time.Since(start).Milliseconds()}
	if errFetch != nil {
		result.Error = errFetch.Error()
		return result
	}
	result.Stage = "proxy"
	result.SourceProxyCount = len(proxies)
	result.ProxyURL = proxies[0]
	latency, errProbe := probe(ctx, proxies[0])
	result.LatencyMs = latency
	result.Success = errProbe == nil
	if errProbe != nil {
		result.Error = errProbe.Error()
	}
	return result
}
