package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/turnstate"
)

const codexProxyAuthTimeout = 15 * time.Second

var (
	externalOAuthQuerySecretPattern = regexp.MustCompile(`(?i)(code|state|access_token|refresh_token|id_token)=([^&\s]+)`)
	externalOAuthJSONSecretPattern  = regexp.MustCompile(`(?i)("(?:code|state|access_token|refresh_token|id_token)"\s*:\s*")[^"]+`)
	externalBearerSecretPattern     = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
)

func externalProxyError(status int, body io.Reader) error {
	raw, _ := io.ReadAll(io.LimitReader(body, 4096))
	var payload struct {
		Error string `json:"error"`
	}
	detail := ""
	if json.Unmarshal(raw, &payload) == nil {
		detail = strings.TrimSpace(payload.Error)
	}
	if detail == "" {
		return fmt.Errorf("独立 codex-proxy 接口返回 HTTP %d", status)
	}
	detail = externalOAuthQuerySecretPattern.ReplaceAllString(detail, "$1=<凭据已隐藏>")
	detail = externalOAuthJSONSecretPattern.ReplaceAllString(detail, "$1<凭据已隐藏>")
	detail = externalBearerSecretPattern.ReplaceAllString(detail, "Bearer <凭据已隐藏>")
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 1000 {
		detail = detail[:1000] + "…"
	}
	return fmt.Errorf("独立 codex-proxy 接口返回 HTTP %d：%s", status, detail)
}

func (h *Handler) codexProxyExternalRequest(ctx context.Context, method, path string, payload any, result any) error {
	h.mu.Lock()
	probe := h.cfg.Codex.TurnState.Probe
	h.mu.Unlock()
	if probe.AuthSource != "external" || strings.TrimSpace(probe.BaseURL) == "" || strings.TrimSpace(probe.APIKey) == "" {
		return fmt.Errorf("请先保存独立 codex-proxy 接口配置")
	}
	base, err := url.Parse(strings.TrimSpace(probe.BaseURL))
	if err != nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return fmt.Errorf("独立 codex-proxy 接口地址无效")
	}
	target := (&url.URL{Scheme: base.Scheme, Host: base.Host, Path: path}).String()
	var body io.Reader
	if payload != nil {
		encoded, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			return fmt.Errorf("无法生成独立接口请求")
		}
		body = bytes.NewReader(encoded)
	}
	req, errRequest := http.NewRequestWithContext(ctx, method, target, body)
	if errRequest != nil {
		return fmt.Errorf("无法创建独立接口请求")
	}
	req.Header.Set("Authorization", "Bearer "+probe.APIKey)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Timeout:   codexProxyAuthTimeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return fmt.Errorf("无法连接独立 codex-proxy 接口")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return externalProxyError(resp.StatusCode, resp.Body)
	}
	if errDecode := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(result); errDecode != nil {
		return fmt.Errorf("独立 codex-proxy 接口返回了无效数据")
	}
	return nil
}

// GetCodexTurnState returns current status, metrics, and tickets for the Codex turn-state subsystem.
func (h *Handler) GetCodexTurnState(c *gin.Context) {
	stats := turnstate.GetManager().Stats()
	c.JSON(http.StatusOK, stats)
}

// GetCodexProbeConfig returns selectable accounts and a redacted external endpoint configuration.
func (h *Handler) GetCodexProbeConfig(c *gin.Context) {
	h.mu.Lock()
	probe := h.cfg.Codex.TurnState.Probe
	h.mu.Unlock()
	source := probe.AuthSource
	if source == "" {
		source = "account"
		if probe.AuthID == "" && probe.APIKey != "" {
			source = "external"
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"auth_source": source, "auth_id": probe.AuthID, "base_url": probe.BaseURL,
		"has_api_key": probe.APIKey != "", "model": probe.Model,
		"accounts": turnstate.ListProbeAccounts(h.authManager),
	})
}

// GetCodexProxyExternalAccount returns a redacted account summary from the configured codex-proxy.
func (h *Handler) GetCodexProxyExternalAccount(c *gin.Context) {
	var result struct {
		Authenticated bool `json:"authenticated"`
		Pool          struct {
			Total  int `json:"total"`
			Active int `json:"active"`
		} `json:"pool"`
	}
	if err := h.codexProxyExternalRequest(c.Request.Context(), http.MethodGet, "/auth/status", nil, &result); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"authenticated":   result.Authenticated,
		"total_accounts":  result.Pool.Total,
		"active_accounts": result.Pool.Active,
	})
}

// StartCodexProxyExternalLogin creates an OAuth session without exposing the codex-proxy API key.
func (h *Handler) StartCodexProxyExternalLogin(c *gin.Context) {
	var result struct {
		AuthURL string `json:"authUrl"`
		State   string `json:"state"`
	}
	if err := h.codexProxyExternalRequest(c.Request.Context(), http.MethodPost, "/auth/login-start", struct{}{}, &result); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	authURL, err := url.Parse(strings.TrimSpace(result.AuthURL))
	if err != nil || authURL.Scheme != "https" || !strings.EqualFold(authURL.Hostname(), "auth.openai.com") {
		c.JSON(http.StatusBadGateway, gin.H{"error": "独立 codex-proxy 返回了不受信任的登录地址"})
		return
	}
	result.State = strings.TrimSpace(result.State)
	if !isSafeOAuthState(result.State) || authURL.Query().Get("state") != result.State {
		c.JSON(http.StatusBadGateway, gin.H{"error": "独立 codex-proxy 返回了无效的登录会话"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"auth_url": authURL.String(), "state": result.State})
}

// StartCodexProxyExternalDeviceLogin creates a device-code login session on the configured codex-proxy.
func (h *Handler) StartCodexProxyExternalDeviceLogin(c *gin.Context) {
	var result struct {
		UserCode                string `json:"userCode"`
		VerificationURI         string `json:"verificationUri"`
		VerificationURIComplete string `json:"verificationUriComplete"`
		DeviceCode              string `json:"deviceCode"`
		ExpiresIn               int    `json:"expiresIn"`
		Interval                int    `json:"interval"`
	}
	if err := h.codexProxyExternalRequest(c.Request.Context(), http.MethodPost, "/auth/device-login", struct{}{}, &result); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	verificationURL, err := url.Parse(strings.TrimSpace(result.VerificationURI))
	if err != nil || verificationURL.Scheme != "https" || !strings.EqualFold(verificationURL.Hostname(), "auth.openai.com") || strings.TrimSpace(result.UserCode) == "" || !isSafeDeviceCode(result.DeviceCode) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "独立 codex-proxy 返回了无效的设备登录信息"})
		return
	}
	completeURL := strings.TrimSpace(result.VerificationURIComplete)
	if completeURL != "" {
		parsedComplete, errComplete := url.Parse(completeURL)
		if errComplete != nil || parsedComplete.Scheme != "https" || !strings.EqualFold(parsedComplete.Hostname(), "auth.openai.com") {
			completeURL = ""
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"user_code": result.UserCode, "verification_uri": verificationURL.String(),
		"verification_uri_complete": completeURL, "device_code": result.DeviceCode,
		"expires_in": result.ExpiresIn, "interval": result.Interval,
	})
}

// PollCodexProxyExternalDeviceLogin checks whether the device-code authorization completed.
func (h *Handler) PollCodexProxyExternalDeviceLogin(c *gin.Context) {
	var body struct {
		DeviceCode string `json:"device_code"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "设备登录会话无效，请重新开始登录"})
		return
	}
	body.DeviceCode = strings.TrimSpace(body.DeviceCode)
	if !isSafeDeviceCode(body.DeviceCode) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "设备登录会话无效，请重新开始登录"})
		return
	}
	var result struct {
		Success bool   `json:"success"`
		Pending bool   `json:"pending"`
		Code    string `json:"code"`
	}
	path := "/auth/device-poll/" + body.DeviceCode
	if err := h.codexProxyExternalRequest(c.Request.Context(), http.MethodGet, path, nil, &result); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": result.Success, "pending": result.Pending, "code": result.Code})
}

func isSafeDeviceCode(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 8 || len(value) > 512 {
		return false
	}
	for _, ch := range value {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' && ch != '.' {
			return false
		}
	}
	return true
}

// CompleteCodexProxyExternalLogin relays a loopback OAuth callback URL to the configured codex-proxy.
func (h *Handler) CompleteCodexProxyExternalLogin(c *gin.Context) {
	var body struct {
		CallbackURL string `json:"callback_url"`
		State       string `json:"state"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请粘贴完整的 OAuth 回调地址"})
		return
	}
	callback, err := url.Parse(strings.TrimSpace(body.CallbackURL))
	if err != nil || callback == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "回调地址无效，请复制登录后浏览器地址栏中的完整 localhost:1455 地址"})
		return
	}
	hostIP := net.ParseIP(callback.Hostname())
	isLoopback := strings.EqualFold(callback.Hostname(), "localhost") || (hostIP != nil && hostIP.IsLoopback())
	body.State = strings.TrimSpace(body.State)
	query := callback.Query()
	callbackState := strings.TrimSpace(query.Get("state"))
	if callbackState == "" && isSafeOAuthState(body.State) {
		query.Set("state", body.State)
		callback.RawQuery = query.Encode()
		callbackState = body.State
	}
	if callback.Scheme != "http" || !isLoopback || callback.Port() != "1455" || callback.Path != "/auth/callback" || query.Get("code") == "" || !isSafeOAuthState(callbackState) || (body.State != "" && callbackState != body.State) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "回调地址无效，请复制登录后浏览器地址栏中的完整 localhost:1455 地址"})
		return
	}
	var result struct {
		Success bool `json:"success"`
	}
	if errRelay := h.codexProxyExternalRequest(c.Request.Context(), http.MethodPost, "/auth/code-relay", gin.H{"callbackUrl": callback.String()}, &result); errRelay != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": errRelay.Error()})
		return
	}
	if !result.Success {
		c.JSON(http.StatusBadGateway, gin.H{"error": "独立 codex-proxy 未确认登录成功"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func isSafeOAuthState(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 16 || len(value) > 256 {
		return false
	}
	for _, ch := range value {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' {
			return false
		}
	}
	return true
}

// PutCodexProbeConfig updates only the probe source and restores it if persistence fails.
func (h *Handler) PutCodexProbeConfig(c *gin.Context) {
	var body struct {
		AuthSource string  `json:"auth_source"`
		AuthID     string  `json:"auth_id"`
		BaseURL    string  `json:"base_url"`
		APIKey     *string `json:"api_key"`
		Model      string  `json:"model"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "探针配置格式错误"})
		return
	}
	body.AuthID, body.BaseURL, body.Model = strings.TrimSpace(body.AuthID), strings.TrimRight(strings.TrimSpace(body.BaseURL), "/"), strings.TrimSpace(body.Model)
	if body.AuthSource != "account" && body.AuthSource != "external" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择账户或独立接口来源"})
		return
	}
	if body.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写探测模型"})
		return
	}
	if body.AuthSource == "account" {
		if _, _, _, err := turnstate.ResolveProbeAccount(h.authManager, body.AuthID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	} else {
		u, err := url.Parse(body.BaseURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "独立接口地址须为不含凭据或查询参数的 HTTP/HTTPS 基础地址"})
			return
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	previous := h.cfg.Codex.TurnState.Probe
	probe := previous
	probe.AuthSource, probe.Model = body.AuthSource, body.Model
	if body.AuthSource == "account" {
		probe.AuthID = body.AuthID
	} else {
		probe.BaseURL = body.BaseURL
		if body.APIKey != nil {
			probe.APIKey = strings.TrimSpace(*body.APIKey)
		}
		if probe.APIKey == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请填写独立接口密钥"})
			return
		}
	}
	h.cfg.Codex.TurnState.Probe = probe
	if !h.persistLocked(c) {
		h.cfg.Codex.TurnState.Probe = previous
		return
	}
	turnstate.GetManager().UpdateConfig(h.cfg)
}

// ProbeCodexTurnState triggers an immediate probe execution via the next available proxy.
func (h *Handler) ProbeCodexTurnState(c *gin.Context) {
	ticket, err := turnstate.GetManager().ProbeOnce(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"ticket": gin.H{
			"length": ticket.Length, "auth_id": ticket.AuthID, "model": ticket.Model,
			"gateway_pool": ticket.GatewayPool, "gateway_proxy": ticket.GatewayProxy,
			"acquired_at": ticket.AcquiredAt, "expires_at": ticket.ExpiresAt,
		},
	})
}
