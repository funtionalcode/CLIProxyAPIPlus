package management

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	geminiAuth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/gemini"
	gitlabauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/gitlab"
	iflowauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/iflow"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kilo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialweight"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var lastRefreshKeys = []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"}

var authFileLocalMetadataKeys = []string{
	"priority",
	"weight",
	"note",
	"prefix",
	"proxy_url",
	"headers",
	"websockets",
	"disabled",
	"tool_prefix_disabled",
	"tool-prefix-disabled",
	"disable_cooling",
	"disable-cooling",
	"request_retry",
	"request-retry",
	"excluded_models",
	"excluded-models",
}

var authFileModelAliasMetadataKeys = []string{"model_aliases", "model-aliases"}

const (
	anthropicCallbackPort            = 54545
	geminiCallbackPort               = 8085
	codexCallbackPort                = codex.DefaultCallbackPort
	geminiCLIEndpoint                = "https://cloudcode-pa.googleapis.com"
	geminiCLIVersion                 = "v1internal"
	authFileWriteIdentitiesHeader    = "X-CPAMP-Auth-File-Write-Identities"
	authFileWriteContentSHA256Header = "X-CPAMP-Auth-File-Write-Content-SHA256"
	gitLabLoginModeOAuth             = "oauth"
	gitLabLoginModePAT               = "pat"
	defaultAuthFilesPage             = 1
	defaultAuthFilesPageSize         = 200
	maximumAuthFilesPageSize         = 1000
)

type authFileListOptions struct {
	paginated       bool
	page            int
	pageSize        int
	includeBalances bool
}

type authFilePageBounds struct {
	page       int
	totalPages int
	start      int
	end        int
}

type callbackForwarder struct {
	provider string
	server   *http.Server
	done     chan struct{}
}

type codexOAuthService interface {
	GenerateAuthURL(state string, pkceCodes *codex.PKCECodes) (string, error)
	ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *codex.PKCECodes) (*codex.CodexAuthBundle, error)
	CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage
}

type codexRedirectOAuthService interface {
	GenerateAuthURLWithRedirect(state string, pkceCodes *codex.PKCECodes, redirectURI string) (string, error)
	ExchangeCodeForTokensWithRedirect(ctx context.Context, code, redirectURI string, pkceCodes *codex.PKCECodes) (*codex.CodexAuthBundle, error)
}

var (
	callbackForwardersMu     sync.Mutex
	callbackForwarders       = make(map[int]*callbackForwarder)
	authFileEntryMu          sync.Mutex
	errAuthFileMustBeJSON    = errors.New("auth file must be .json")
	errAuthFileNotFound      = errors.New("auth file not found")
	errAuthFileWriteConflict = errors.New("auth file changed; reload and retry")
	errPluginVirtualAuth     = errors.New("plugin virtual auth cannot be modified directly; edit or delete the source auth file")
	newCodexOAuthService     = func(cfg *config.Config) codexOAuthService { return codex.NewCodexAuth(cfg) }
)

type authFileWriteGuard struct {
	expectedContentSHA256 string
	identities            []authFileWriteIdentity
}

type authFileWriteIdentity struct {
	Name string `json:"name"`
}

func extractLastRefreshTimestamp(meta map[string]any) (time.Time, bool) {
	if len(meta) == 0 {
		return time.Time{}, false
	}
	for _, key := range lastRefreshKeys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseLastRefreshValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func parseLastRefreshValue(v any) (time.Time, bool) {
	switch val := v.(type) {
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00"}
		for _, layout := range layouts {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts.UTC(), true
			}
		}
		if unix, err := strconv.ParseInt(s, 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0).UTC(), true
		}
	case float64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case int64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(val, 0).UTC(), true
	case int:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case json.Number:
		if i, err := val.Int64(); err == nil && i > 0 {
			return time.Unix(i, 0).UTC(), true
		}
	}
	return time.Time{}, false
}

func isWebUIRequest(c *gin.Context) bool {
	raw := strings.TrimSpace(c.Query("is_webui"))
	if raw == "" {
		return false
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func startCallbackForwarder(port int, provider, targetBase string) (*callbackForwarder, error) {
	return startCallbackForwarderOn("127.0.0.1", port, provider, targetBase)
}

func startCallbackForwarderOn(bindHost string, port int, provider, targetBase string) (*callbackForwarder, error) {
	callbackForwardersMu.Lock()
	prev := callbackForwarders[port]
	if prev != nil {
		delete(callbackForwarders, port)
	}
	callbackForwardersMu.Unlock()

	if prev != nil {
		stopForwarderInstance(port, prev)
	}

	bindHost = strings.TrimSpace(bindHost)
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}

	addr := net.JoinHostPort(bindHost, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := targetBase
		if raw := r.URL.RawQuery; raw != "" {
			if strings.Contains(target, "?") {
				target = target + "&" + raw
			} else {
				target = target + "?" + raw
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target, http.StatusFound)
	})

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	done := make(chan struct{})

	go func() {
		if errServe := srv.Serve(ln); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			log.WithError(errServe).Warnf("callback forwarder for %s stopped unexpectedly", provider)
		}
		close(done)
	}()

	forwarder := &callbackForwarder{
		provider: provider,
		server:   srv,
		done:     done,
	}

	callbackForwardersMu.Lock()
	callbackForwarders[port] = forwarder
	callbackForwardersMu.Unlock()

	log.Infof("callback forwarder for %s listening on %s", provider, addr)

	return forwarder, nil
}

func stopCallbackForwarderInstance(port int, forwarder *callbackForwarder) {
	if forwarder == nil {
		return
	}
	callbackForwardersMu.Lock()
	if current := callbackForwarders[port]; current == forwarder {
		delete(callbackForwarders, port)
	}
	callbackForwardersMu.Unlock()

	stopForwarderInstance(port, forwarder)
}

func stopForwarderInstance(port int, forwarder *callbackForwarder) {
	if forwarder == nil || forwarder.server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := forwarder.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.WithError(err).Warnf("failed to shut down callback forwarder on port %d", port)
	}

	select {
	case <-forwarder.done:
	case <-time.After(2 * time.Second):
	}

	log.Infof("callback forwarder on port %d stopped", port)
}

func (h *Handler) managementCallbackURL(path string) (string, error) {
	if h == nil || h.cfg == nil || h.cfg.Port <= 0 {
		return "", fmt.Errorf("server port is not configured")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	scheme := "http"
	if h.cfg.TLS.Enable {
		scheme = "https"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, h.cfg.Port, path), nil
}

func normalizePublicBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("public base URL is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid public base URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid public base URL: missing scheme or host")
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String(), nil
}

func requestPublicBaseURL(c *gin.Context) (string, error) {
	if c == nil || c.Request == nil {
		return "", fmt.Errorf("request is not available")
	}

	scheme := "http"
	if proto := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0]); proto != "" {
		scheme = proto
	} else if c.Request.TLS != nil {
		scheme = "https"
	}

	host := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Host"), ",")[0])
	if host == "" {
		host = strings.TrimSpace(c.Request.Host)
	}
	if host == "" {
		return "", fmt.Errorf("request host is empty")
	}

	if forwardedPort := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Port"), ",")[0]); forwardedPort != "" && !strings.Contains(host, ":") {
		if !((scheme == "http" && forwardedPort == "80") || (scheme == "https" && forwardedPort == "443")) {
			host = net.JoinHostPort(host, forwardedPort)
		}
	}

	return (&url.URL{Scheme: scheme, Host: host}).String(), nil
}

func joinPublicBaseURL(baseURL, path string) (string, error) {
	normalizedBaseURL, err := normalizePublicBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(normalizedBaseURL)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (h *Handler) codexPublicBaseURL(c *gin.Context) (string, error) {
	if h != nil && h.cfg != nil {
		if baseURL := strings.TrimSpace(h.cfg.CodexOAuth.PublicBaseURL); baseURL != "" {
			return normalizePublicBaseURL(baseURL)
		}
	}
	return requestPublicBaseURL(c)
}

func (h *Handler) codexPublicCallbackPort() int {
	if h != nil && h.cfg != nil && h.cfg.CodexOAuth.PublicCallbackPort > 0 {
		return h.cfg.CodexOAuth.PublicCallbackPort
	}
	return codex.DefaultCallbackPort
}

func (h *Handler) codexCallbackBindHost() string {
	if h != nil && h.cfg != nil {
		if bindHost := strings.TrimSpace(h.cfg.CodexOAuth.BindHost); bindHost != "" {
			return bindHost
		}
	}
	return "127.0.0.1"
}

func (h *Handler) codexRedirectURL(c *gin.Context) (string, error) {
	if h != nil && h.cfg != nil {
		if redirectURL := strings.TrimSpace(h.cfg.CodexOAuth.RedirectURL); redirectURL != "" {
			if normalized, err := url.Parse(redirectURL); err == nil && normalized.Scheme != "" && normalized.Host != "" {
				return normalized.String(), nil
			}
			return "", fmt.Errorf("invalid codex redirect URL")
		}
	}
	publicBaseURL, err := h.codexPublicBaseURL(c)
	if err != nil {
		return "", err
	}
	return codex.RedirectURIForPublicBase(publicBaseURL, h.codexPublicCallbackPort())
}

func (h *Handler) codexManagementCallbackURL(c *gin.Context, path string) (string, error) {
	publicBaseURL, err := h.codexPublicBaseURL(c)
	if err != nil {
		return "", err
	}
	return joinPublicBaseURL(publicBaseURL, path)
}

func pluginAuthProviderFromPath(path string) (string, bool) {
	path = strings.TrimSpace(path)
	const prefix = "/v0/management/"
	const suffix = "-auth-url"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	provider := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return "", false
	}
	for _, r := range provider {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return "", false
		}
	}
	return provider, true
}

func (h *Handler) ServePluginAuthURL(c *gin.Context) bool {
	if h == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	if host == nil {
		return false
	}
	provider, ok := pluginAuthProviderFromPath(c.Request.URL.Path)
	if !ok || !host.HasAuthProvider(provider) {
		return false
	}

	ctx := PopulateAuthContext(context.Background(), c)
	baseURL, errBaseURL := h.managementCallbackURL("/v0/management/oauth-callback")
	if errBaseURL != nil {
		log.WithError(errBaseURL).Error("failed to compute plugin auth callback URL")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return true
	}
	resp, handled, errStart := host.StartLogin(ctx, provider, baseURL)
	if !handled {
		return false
	}
	if errStart != nil {
		log.WithError(errStart).Error("failed to start plugin auth login")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return true
	}
	state := strings.TrimSpace(resp.State)
	if state == "" {
		log.WithField("provider", provider).Error("plugin auth provider returned empty state")
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid oauth state"})
		return true
	}
	if errState := ValidateOAuthState(state); errState != nil {
		log.WithError(errState).WithField("provider", provider).Error("plugin auth provider returned invalid state")
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid oauth state"})
		return true
	}
	if errRegister := RegisterPluginOAuthSession(state, provider, resp.Metadata); errRegister != nil {
		log.WithError(errRegister).WithField("provider", provider).Error("failed to register plugin oauth session")
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to generate authorization url"})
		return true
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": resp.URL, "state": state})
	return true
}

// ListAuthFiles returns auth metadata. page/page_size enable bounded responses;
// include_balances=true explicitly opts into synchronous remote balance lookups.
func (h *Handler) ListAuthFiles(c *gin.Context) {
	if h == nil {
		c.JSON(500, gin.H{"error": "handler not initialized"})
		return
	}
	options, errOptions := parseAuthFileListOptions(c)
	if errOptions != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errOptions.Error()})
		return
	}
	if h.authManager == nil {
		h.listAuthFilesFromDisk(c, options)
		return
	}
	nameFilter := strings.TrimSpace(c.Query("name"))
	authIndexFilter := strings.TrimSpace(c.Query("auth_index"))
	auths := filterAuthFilesForLookup(h.authManager.List(), nameFilter, authIndexFilter)
	if options.paginated {
		auths = preparePaginatedAuthFiles(auths)
		bounds := calculateAuthFilePageBounds(len(auths), options)
		files := make([]gin.H, 0, bounds.end-bounds.start)
		for _, auth := range auths[bounds.start:bounds.end] {
			if entry := h.buildAuthFileEntry(auth, options.includeBalances); entry != nil {
				files = append(files, entry)
			}
		}
		writeAuthFilesResponse(c, files, len(auths), bounds, options)
		return
	}
	files := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if entry := h.buildAuthFileEntry(auth, options.includeBalances); entry != nil {
			files = append(files, entry)
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		return compareAuthFileEntries(files[i], files[j]) < 0
	})
	c.JSON(200, gin.H{"files": files})
}

func parseAuthFileListOptions(c *gin.Context) (authFileListOptions, error) {
	options := authFileListOptions{page: defaultAuthFilesPage, pageSize: defaultAuthFilesPageSize}
	rawPage, hasPage := c.GetQuery("page")
	rawPageSize, hasPageSize := c.GetQuery("page_size")
	if hasPage || hasPageSize {
		options.paginated = true
	}
	if hasPage {
		page, errParse := strconv.Atoi(strings.TrimSpace(rawPage))
		if errParse != nil || page < 1 {
			return authFileListOptions{}, errors.New("page must be a positive integer")
		}
		options.page = page
	}
	if hasPageSize {
		pageSize, errParse := strconv.Atoi(strings.TrimSpace(rawPageSize))
		if errParse != nil || pageSize < 1 || pageSize > maximumAuthFilesPageSize {
			return authFileListOptions{}, fmt.Errorf("page_size must be between 1 and %d", maximumAuthFilesPageSize)
		}
		options.pageSize = pageSize
	}
	if rawIncludeBalances, ok := c.GetQuery("include_balances"); ok {
		includeBalances, errParse := strconv.ParseBool(strings.TrimSpace(rawIncludeBalances))
		if errParse != nil {
			return authFileListOptions{}, errors.New("include_balances must be a boolean")
		}
		options.includeBalances = includeBalances
	}
	return options, nil
}

func preparePaginatedAuthFiles(auths []*coreauth.Auth) []*coreauth.Auth {
	filtered := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		auth.EnsureIndex()
		runtimeOnly := isRuntimeOnlyAuth(auth)
		if runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled) {
			continue
		}
		if !runtimeOnly && strings.TrimSpace(authAttribute(auth, "path")) == "" {
			continue
		}
		filtered = append(filtered, auth)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		leftProvider := strings.ToLower(strings.TrimSpace(filtered[i].Provider))
		rightProvider := strings.ToLower(strings.TrimSpace(filtered[j].Provider))
		if providerCompare := strings.Compare(leftProvider, rightProvider); providerCompare != 0 {
			return providerCompare < 0
		}
		leftName := authFileListName(filtered[i])
		rightName := authFileListName(filtered[j])
		if nameCompare := strings.Compare(strings.ToLower(leftName), strings.ToLower(rightName)); nameCompare != 0 {
			return nameCompare < 0
		}
		return strings.Compare(strings.TrimSpace(filtered[i].Index), strings.TrimSpace(filtered[j].Index)) < 0
	})
	return filtered
}

func authFileListName(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if name := strings.TrimSpace(auth.FileName); name != "" {
		return name
	}
	return strings.TrimSpace(auth.ID)
}

func calculateAuthFilePageBounds(total int, options authFileListOptions) authFilePageBounds {
	totalPages := 0
	if total > 0 {
		totalPages = (total + options.pageSize - 1) / options.pageSize
	}
	page := options.page
	if totalPages > 0 && page > totalPages {
		page = totalPages
	}
	start := 0
	end := 0
	if total > 0 {
		start = (page - 1) * options.pageSize
		end = start + options.pageSize
		if end > total {
			end = total
		}
	}
	return authFilePageBounds{page: page, totalPages: totalPages, start: start, end: end}
}

func writeAuthFilesResponse(c *gin.Context, files []gin.H, total int, bounds authFilePageBounds, options authFileListOptions) {
	c.JSON(http.StatusOK, gin.H{
		"files":       files,
		"page":        bounds.page,
		"page_size":   options.pageSize,
		"total":       total,
		"total_pages": bounds.totalPages,
	})
}

func filterAuthFilesForLookup(auths []*coreauth.Auth, name string, authIndex string) []*coreauth.Auth {
	name = strings.TrimSpace(name)
	authIndex = strings.TrimSpace(authIndex)
	if name == "" && authIndex == "" {
		return auths
	}
	filtered := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if matchesAuthFileLookup(auth, name, authIndex) {
			filtered = append(filtered, auth)
		}
	}
	return filtered
}

func lockedAuthIndex(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	authFileEntryMu.Lock()
	defer authFileEntryMu.Unlock()
	return strings.TrimSpace(auth.EnsureIndex())
}

func matchesAuthFileLookup(auth *coreauth.Auth, name string, authIndex string) bool {
	if auth == nil {
		return false
	}
	if name != "" && strings.TrimSpace(auth.ID) != name && strings.TrimSpace(auth.FileName) != name {
		return false
	}
	if authIndex != "" && lockedAuthIndex(auth) != authIndex {
		return false
	}
	return true
}

func (h *Handler) lookupAuthFile(name string, authIndex string) (*coreauth.Auth, bool) {
	name = strings.TrimSpace(name)
	authIndex = strings.TrimSpace(authIndex)
	if h == nil || h.authManager == nil || name == "" {
		return nil, false
	}
	if authIndex == "" {
		if auth, ok := h.authManager.GetByID(name); ok {
			return auth, true
		}
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth != nil && strings.TrimSpace(auth.FileName) == name {
				return auth, true
			}
		}
		return nil, false
	}
	auths := h.authManager.List()
	for _, auth := range auths {
		if matchesAuthFileLookup(auth, name, authIndex) {
			return auth, true
		}
	}
	return nil, false
}

// GetAuthFileModels returns the models supported by a specific auth file
func (h *Handler) GetAuthFileModels(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		c.JSON(400, gin.H{"error": "name is required"})
		return
	}

	// Try to find auth ID via authManager
	var authID string
	if h.authManager != nil {
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth.FileName == name || auth.ID == name {
				authID = auth.ID
				break
			}
		}
	}

	if authID == "" {
		authID = name // fallback to filename as ID
	}

	// Get models from registry
	reg := registry.GetGlobalRegistry()
	models := reg.GetModelsForClient(authID)

	result := make([]gin.H, 0, len(models))
	for _, m := range models {
		entry := gin.H{
			"id": m.ID,
		}
		if m.DisplayName != "" {
			entry["display_name"] = m.DisplayName
		}
		if m.Type != "" {
			entry["type"] = m.Type
		}
		if m.OwnedBy != "" {
			entry["owned_by"] = m.OwnedBy
		}
		result = append(result, entry)
	}

	c.JSON(200, gin.H{"models": result})
}

// List auth files from disk when the auth manager is unavailable.
func (h *Handler) listAuthFilesFromDisk(c *gin.Context, options authFileListOptions) {
	nameFilter := strings.TrimSpace(c.Query("name"))
	authIndexFilter := strings.TrimSpace(c.Query("auth_index"))
	entries, err := os.ReadDir(h.cfg.AuthDir)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read auth dir: %v", err)})
		return
	}
	jsonEntries := make([]os.DirEntry, 0, len(entries))
	if authIndexFilter != "" {
		bounds := calculateAuthFilePageBounds(0, options)
		if options.paginated {
			writeAuthFilesResponse(c, []gin.H{}, 0, bounds, options)
			return
		}
		c.JSON(200, gin.H{"files": []gin.H{}})
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if nameFilter != "" && name != nameFilter {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		jsonEntries = append(jsonEntries, entry)
	}
	sort.SliceStable(jsonEntries, func(i, j int) bool {
		return strings.Compare(strings.ToLower(jsonEntries[i].Name()), strings.ToLower(jsonEntries[j].Name())) < 0
	})
	selectedEntries := jsonEntries
	var bounds authFilePageBounds
	if options.paginated {
		bounds = calculateAuthFilePageBounds(len(jsonEntries), options)
		selectedEntries = jsonEntries[bounds.start:bounds.end]
	}
	files := make([]gin.H, 0, len(selectedEntries))
	for _, e := range selectedEntries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		if info, errInfo := e.Info(); errInfo == nil {
			fileData := gin.H{"name": name, "size": info.Size(), "modtime": info.ModTime()}

			// Read file to get type field
			full := filepath.Join(h.cfg.AuthDir, name)
			if data, errRead := os.ReadFile(full); errRead == nil {
				typeValue := gjson.GetBytes(data, "type").String()
				emailValue := gjson.GetBytes(data, "email").String()
				fileData["type"] = typeValue
				fileData["email"] = emailValue
				if projectID := strings.TrimSpace(gjson.GetBytes(data, "project_id").String()); projectID != "" {
					fileData["project_id"] = projectID
				}
				for _, key := range []string{"group", "plan_type", "plan", "account_type"} {
					if value := strings.TrimSpace(gjson.GetBytes(data, key).String()); value != "" {
						fileData[key] = value
					}
				}
				if value := strings.TrimSpace(gjson.GetBytes(data, "balance.group").String()); value != "" {
					fileData["balance"] = gin.H{"group": value}
				}
				if pv := gjson.GetBytes(data, "priority"); pv.Exists() {
					switch pv.Type {
					case gjson.Number:
						fileData["priority"] = int(pv.Int())
					case gjson.String:
						if parsed, errAtoi := strconv.Atoi(strings.TrimSpace(pv.String())); errAtoi == nil {
							fileData["priority"] = parsed
						}
					}
				}
				if wv := gjson.GetBytes(data, coreauth.AttributeWeight); wv.Exists() {
					var rawWeight string
					switch wv.Type {
					case gjson.Number:
						rawWeight = wv.Raw
					case gjson.String:
						rawWeight = wv.String()
					}
					if rawWeight != "" {
						if weight, errWeight := credentialweight.ParseString(rawWeight); errWeight == nil {
							fileData[coreauth.AttributeWeight] = weight
						}
					}
				}
				if nv := gjson.GetBytes(data, "note"); nv.Exists() && nv.Type == gjson.String {
					if trimmed := strings.TrimSpace(nv.String()); trimmed != "" {
						fileData["note"] = trimmed
					}
				}
				if pv := gjson.GetBytes(data, "prefix"); pv.Exists() && pv.Type == gjson.String {
					if trimmed := strings.TrimSpace(pv.String()); trimmed != "" {
						fileData["prefix"] = trimmed
					}
				}
				if pv := gjson.GetBytes(data, "proxy_url"); pv.Exists() && pv.Type == gjson.String {
					if trimmed := strings.TrimSpace(pv.String()); trimmed != "" {
						fileData["proxy_url"] = trimmed
					}
				}
				if wv := gjson.GetBytes(data, "websockets"); wv.Exists() {
					switch wv.Type {
					case gjson.True:
						fileData["websockets"] = true
					case gjson.False:
						fileData["websockets"] = false
					case gjson.String:
						if parsed, errParse := strconv.ParseBool(strings.TrimSpace(wv.String())); errParse == nil {
							fileData["websockets"] = parsed
						}
					}
				}
				if requestRetry, okRetry := authFileRequestRetryFromJSON(data); okRetry {
					fileData["request_retry"] = requestRetry
				}
			}

			files = append(files, fileData)
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		return compareAuthFileEntries(files[i], files[j]) < 0
	})
	if options.paginated {
		writeAuthFilesResponse(c, files, len(jsonEntries), bounds, options)
		return
	}
	c.JSON(200, gin.H{"files": files})
}

func normalizeAuthFilePlan(value string) string {
	return strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.TrimSpace(value)))
}

func authFilePlanRank(value string) int {
	switch normalizeAuthFilePlan(value) {
	case "pro20x":
		return 0
	case "pro5x":
		return 1
	case "plus":
		return 2
	case "free":
		return 3
	default:
		return 4
	}
}

func authFilePlanValue(file gin.H) string {
	for _, key := range []string{"group", "plan_type", "plan", "account_type"} {
		if value := strings.TrimSpace(fmt.Sprint(file[key])); value != "" && value != "<nil>" {
			return value
		}
	}

	if balance, ok := file["balance"].(gin.H); ok {
		if value := strings.TrimSpace(fmt.Sprint(balance["group"])); value != "" && value != "<nil>" {
			return value
		}
	}
	if balance, ok := file["balance"].(map[string]any); ok {
		if value := strings.TrimSpace(fmt.Sprint(balance["group"])); value != "" && value != "<nil>" {
			return value
		}
	}
	if idToken, ok := file["id_token"].(map[string]any); ok {
		if value := strings.TrimSpace(fmt.Sprint(idToken["plan_type"])); value != "" && value != "<nil>" {
			return value
		}
	}

	return ""
}

func compareAuthFileEntries(left gin.H, right gin.H) int {
	leftPlan := authFilePlanValue(left)
	rightPlan := authFilePlanValue(right)
	leftRank := authFilePlanRank(leftPlan)
	rightRank := authFilePlanRank(rightPlan)
	if leftRank != rightRank {
		return leftRank - rightRank
	}
	if leftRank == 4 && rightRank == 4 {
		if planCompare := strings.Compare(strings.ToLower(leftPlan), strings.ToLower(rightPlan)); planCompare != 0 {
			return planCompare
		}
	}

	nameI, _ := left["name"].(string)
	nameJ, _ := right["name"].(string)
	return strings.Compare(strings.ToLower(nameI), strings.ToLower(nameJ))
}

func (h *Handler) buildAuthFileEntry(auth *coreauth.Auth, includeBalances bool) gin.H {
	authFileEntryMu.Lock()
	defer authFileEntryMu.Unlock()
	return h.buildAuthFileEntryLocked(auth, includeBalances)
}

func (h *Handler) buildAuthFileEntryLocked(auth *coreauth.Auth, includeBalances bool) gin.H {
	if auth == nil {
		return nil
	}
	auth.EnsureIndex()
	runtimeOnly := isRuntimeOnlyAuth(auth)
	if runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled) {
		return nil
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	if path == "" && !runtimeOnly {
		return nil
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	entry := gin.H{
		"id":             auth.ID,
		"auth_index":     auth.Index,
		"name":           name,
		"type":           strings.TrimSpace(auth.Provider),
		"provider":       strings.TrimSpace(auth.Provider),
		"label":          auth.Label,
		"status":         auth.Status,
		"status_message": auth.StatusMessage,
		"disabled":       auth.Disabled,
		"unavailable":    auth.Unavailable,
		"runtime_only":   runtimeOnly,
		"source":         "memory",
		"size":           int64(0),
	}
	entry["success"] = auth.Success
	entry["failed"] = auth.Failed
	entry["recent_requests"] = auth.RecentRequestsSnapshot(time.Now())
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		entry["claude_header_policy"] = helps.ClaudeHeaderPassthroughPolicy()
		entry["claude_header_observations"] = helps.RecentClaudeHeaderObservations()
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		entry["codex_header_policy"] = helps.CodexHeaderPolicy()
	}
	entry["quota"] = quotaObservationPayloadForProvider(auth.Provider, auth.Quota)
	if modelQuotas := modelQuotaObservationPayload(auth.Provider, auth.ModelStates); len(modelQuotas) > 0 {
		entry["model_quotas"] = modelQuotas
	}
	if email := authEmail(auth); email != "" {
		entry["email"] = email
	}
	if projectID := authProjectID(auth); projectID != "" {
		entry["project_id"] = projectID
	}
	if accountType, account := auth.AccountInfo(); accountType != "" || account != "" {
		if accountType != "" {
			entry["account_type"] = accountType
		}
		if account != "" {
			entry["account"] = account
		}
	}
	if !auth.CreatedAt.IsZero() {
		entry["created_at"] = auth.CreatedAt
	}
	if !auth.UpdatedAt.IsZero() {
		entry["modtime"] = auth.UpdatedAt
		entry["updated_at"] = auth.UpdatedAt
	}
	if !auth.LastRefreshedAt.IsZero() {
		entry["last_refresh"] = auth.LastRefreshedAt
	}
	if !auth.NextRetryAfter.IsZero() {
		entry["next_retry_after"] = auth.NextRetryAfter
	}
	if path != "" {
		entry["path"] = path
		entry["source"] = "file"
		if info, err := os.Stat(path); err == nil {
			entry["size"] = info.Size()
			entry["modtime"] = info.ModTime()
		} else if os.IsNotExist(err) {
			// Hide credentials removed from disk but still lingering in memory.
			if !runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled || strings.EqualFold(strings.TrimSpace(auth.StatusMessage), "removed via management api")) {
				return nil
			}
			entry["source"] = "memory"
		} else {
			log.WithError(err).Warnf("failed to stat auth file %s", path)
		}
	}
	if claims := extractCodexIDTokenClaims(auth); claims != nil {
		entry["id_token"] = claims
	}
	// Expose priority from Attributes (set by synthesizer from JSON "priority" field).
	// Fall back to Metadata for auths registered via UploadAuthFile (no synthesizer).
	if p := strings.TrimSpace(authAttribute(auth, "priority")); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil {
			entry["priority"] = parsed
		}
	} else if auth.Metadata != nil {
		if rawPriority, ok := auth.Metadata["priority"]; ok {
			switch v := rawPriority.(type) {
			case float64:
				entry["priority"] = int(v)
			case int:
				entry["priority"] = v
			case string:
				if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					entry["priority"] = parsed
				}
			}
		}
	}
	if w := strings.TrimSpace(authAttribute(auth, "weight")); w != "" {
		if parsed, err := strconv.Atoi(w); err == nil {
			entry["weight"] = parsed
		}
	} else if auth.Metadata != nil {
		if rawWeight, ok := auth.Metadata["weight"]; ok {
			switch v := rawWeight.(type) {
			case float64:
				entry["weight"] = int(v)
			case int:
				entry["weight"] = v
			case string:
				if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					entry["weight"] = parsed
				}
			}
		}
	}
	// Expose note from Attributes (set by synthesizer from JSON "note" field).
	// Fall back to Metadata for auths registered via UploadAuthFile (no synthesizer).
	if note := strings.TrimSpace(authAttribute(auth, "note")); note != "" {
		entry["note"] = note
	} else if auth.Metadata != nil {
		if rawNote, ok := auth.Metadata["note"].(string); ok {
			if trimmed := strings.TrimSpace(rawNote); trimmed != "" {
				entry["note"] = trimmed
			}
		}
	}
	// Expose prefix and per-auth proxy URL so the management UI can pre-fill
	// the editor with the values that PatchAuthFileFields previously wrote.
	// Mirrors how API-key list endpoints already surface the per-key proxy.
	if prefix := strings.TrimSpace(auth.Prefix); prefix != "" {
		entry["prefix"] = prefix
	} else if auth.Metadata != nil {
		if rawPrefix, ok := auth.Metadata["prefix"].(string); ok {
			if trimmed := strings.TrimSpace(rawPrefix); trimmed != "" {
				entry["prefix"] = trimmed
			}
		}
	}
	if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
		entry["proxy_url"] = proxyURL
	} else if auth.Metadata != nil {
		if rawProxy, ok := auth.Metadata["proxy_url"].(string); ok {
			if trimmed := strings.TrimSpace(rawProxy); trimmed != "" {
				entry["proxy_url"] = trimmed
			}
		}
	}
	// Surface the openai-compatibility sub-provider key, the user-facing
	// compat name, the upstream baseURL and the api-key used by this auth.
	// The frontend needs all four to (a) recognize ollama/deepseek auths
	// registered through openai-compatibility, and (b) match auths against
	// the GET /api-key-usage response (keyed by provider + "<baseURL>|<apiKey>").
	if v := strings.TrimSpace(authAttribute(auth, "provider_key")); v != "" {
		entry["provider_key"] = v
	}
	if v := strings.TrimSpace(authAttribute(auth, "compat_name")); v != "" {
		entry["compat_name"] = v
	}
	if v := strings.TrimSpace(authAttribute(auth, "base_url")); v != "" {
		entry["base_url"] = v
	}
	if v := strings.TrimSpace(authAttribute(auth, "api_key")); v != "" {
		entry["api_key"] = v
	}
	if weight, ok := authWeightValue(auth); ok {
		entry[coreauth.AttributeWeight] = weight
	}
	if includeBalances {
		h.appendAuthFileBalances(entry, auth)
	}
	if websockets, ok := authWebsocketsValue(auth); ok {
		entry["websockets"] = websockets
	}
	if requestRetry, ok := auth.RequestRetryOverride(); ok {
		entry["request_retry"] = requestRetry
	}
	return entry
}

func (h *Handler) appendAuthFileBalances(entry gin.H, auth *coreauth.Auth) {
	// For ollama providers, fetch and display cloud usage/balance.
	// Tries Bearer (API key / OAuth token) first, then session cookies.
	// Accepts entries registered both as native "ollama" and via
	// openai-compatibility (provider_key / compat_name == "ollama").
	if matchesCompatProvider(auth, "ollama") {
		creds := ollamaCredentialsFromAuth(auth)
		if creds.HasAny() {
			if balance := helps.GetOllamaBalanceWithCreds(creds, h.cfg, auth); balance != nil {
				b := gin.H{}
				b["session_usage_pct"] = balance.SessionUsagePct
				b["weekly_usage_pct"] = balance.WeeklyUsagePct
				if !balance.SessionResetsAt.IsZero() {
					b["session_resets_at"] = balance.SessionResetsAt
				}
				if !balance.WeeklyResetsAt.IsZero() {
					b["weekly_resets_at"] = balance.WeeklyResetsAt
				}
				if balance.Plan != "" {
					b["plan"] = balance.Plan
				}
				if balance.Source != "" {
					b["source"] = balance.Source
				}
				b["fetched_at"] = balance.FetchedAt
				entry["balance"] = b
			}
		}
	}
	// For deepseek providers, fetch wallet balance and monthly cost from
	// platform.deepseek.com/api/v0/users/get_user_summary.
	// Accepts entries registered both as native "deepseek" and via
	// openai-compatibility (provider_key / compat_name == "deepseek").
	if matchesCompatProvider(auth, "deepseek") {
		creds := deepseekCredentialsFromAuth(auth)
		if creds.HasAny() {
			if balance := helps.GetDeepSeekBalanceWithCreds(creds, h.cfg, auth); balance != nil {
				entry["balance"] = deepseekBalanceToGin(balance)
			}
		}
	}
	// For xiaomi providers (platform.xiaomimimo.com), fetch token-plan usage
	// using the per-key Cookie header carried via auth.Attributes["header:Cookie"].
	// Cookie is per api-key (each user/account has its own session), so the
	// credential is sourced from per-key headers, not from provider-level Headers.
	if matchesCompatProvider(auth, "xiaomi") {
		creds := xiaomiCredentialsFromAuth(auth)
		if creds.HasAny() {
			if balance := helps.GetXiaomiBalanceWithCreds(creds, h.cfg, auth); balance != nil {
				entry["balance"] = xiaomiBalanceToGin(balance)
			}
		}
	}
	// For anyrouter providers (anyrouter.top), fetch user/self snapshot using
	// per-key Cookie + new-api-user. Both must be configured under the same
	// api-key entry's headers since they are tied to a single browser session.
	if matchesCompatProvider(auth, "anyrouter") {
		creds := anyrouterCredentialsFromAuth(auth)
		if creds.HasAny() {
			if balance := helps.GetAnyrouterBalanceWithCreds(creds, h.cfg, auth); balance != nil {
				entry["balance"] = anyrouterBalanceToGin(balance)
			}
		}
	}
}

// matchesCompatProvider reports whether the auth entry targets the named
// sub-provider. It accepts the entry both when auth.Provider equals name
// directly (legacy/native ollama or deepseek registrations) and when the
// entry is registered through openai-compatibility — in which case the
// real sub-provider is exposed via Attributes["provider_key"] (synthesizer
// canonical key) or Attributes["compat_name"] (display name from the
// openai-compatibility config block).
//
// This is what unblocks balance fetching for entries that the user adds
// under openai-compatibility (the typical case for ollama and deepseek):
// auth.Provider is "openai-compatibility" there, not "ollama"/"deepseek".
func matchesCompatProvider(auth *coreauth.Auth, name string) bool {
	if auth == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), name) {
		return true
	}
	if auth.Attributes != nil {
		if strings.EqualFold(strings.TrimSpace(auth.Attributes["provider_key"]), name) {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(auth.Attributes["compat_name"]), name) {
			return true
		}
	}
	return false
}

// ollamaCredentialsFromAuth assembles every credential that can be used to
// query the ollama account usage endpoint. Both fields are populated when
// available so the fetcher can fall through if one path fails.
//
// Sources, in priority order for the API key:
//  1. Attributes["api_key"]      — synthesizer-populated for openai-compatibility
//  2. Metadata["access_token"]   — populated by the custom-oauth flow
//  3. Metadata["api_key"]        — uploaded auth files / ad-hoc entries
func ollamaCredentialsFromAuth(auth *coreauth.Auth) helps.OllamaCredentials {
	creds := helps.OllamaCredentials{Cookies: ollamaCookiesFromAuth(auth)}
	if auth == nil {
		return creds
	}
	if auth.Attributes != nil {
		if k := strings.TrimSpace(auth.Attributes["api_key"]); k != "" {
			creds.APIKey = k
		}
	}
	if creds.APIKey == "" && auth.Metadata != nil {
		if tok, ok := auth.Metadata["access_token"].(string); ok {
			if t := strings.TrimSpace(tok); t != "" {
				creds.APIKey = t
			}
		}
		if creds.APIKey == "" {
			if k, ok := auth.Metadata["api_key"].(string); ok {
				if t := strings.TrimSpace(k); t != "" {
					creds.APIKey = t
				}
			}
		}
	}
	return creds
}

// ollamaCookiesFromAuth extracts ollama session cookies from auth metadata and attributes.
// It checks in order:
//  1. Attributes["ollama_cookies"] or Attributes["header:Cookie"] (openai-compatibility entries)
//  2. Metadata["ollama_cookies"] (OAuth/custom entries)
//  3. Individual cookie fields in Metadata: "aid" + "__Secure-session"
func ollamaCookiesFromAuth(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	// Check Attributes first (used by openai-compatibility synthesized entries)
	if auth.Attributes != nil {
		if cookies := strings.TrimSpace(auth.Attributes["ollama_cookies"]); cookies != "" {
			return cookies
		}
		// Check if a Cookie header is set via custom headers (header:Cookie)
		if cookies := strings.TrimSpace(auth.Attributes["header:Cookie"]); cookies != "" {
			return cookies
		}
	}
	// Check Metadata (used by OAuth and custom-oauth entries)
	if auth.Metadata != nil {
		// Prefer the explicit ollama_cookies field (full Cookie header value)
		if cookies, ok := auth.Metadata["ollama_cookies"].(string); ok {
			if trimmed := strings.TrimSpace(cookies); trimmed != "" {
				return trimmed
			}
		}
		// Fall back to combining individual cookie fields from metadata
		var parts []string
		if aid, ok := auth.Metadata["aid"].(string); ok {
			if trimmed := strings.TrimSpace(aid); trimmed != "" {
				parts = append(parts, "aid="+trimmed)
			}
		}
		if session, ok := auth.Metadata["__Secure-session"].(string); ok {
			if trimmed := strings.TrimSpace(session); trimmed != "" {
				parts = append(parts, "__Secure-session="+trimmed)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	return ""
}

func authFileRequestRetryFromJSON(data []byte) (int, bool) {
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
		return 0, false
	}
	return (&coreauth.Auth{Metadata: metadata}).RequestRetryOverride()
}

// quotaObservationPayload exposes only passive provider observations. Cooldown
// fields are intentionally excluded so this management response cannot be
// mistaken for scheduler state or influence scheduling behavior.
func quotaObservationPayloadForProvider(provider string, quota coreauth.QuotaState) gin.H {
	if !coreauth.ProviderSupportsQuotaObservation(provider) {
		return quotaObservationPayload(coreauth.QuotaState{})
	}
	return quotaObservationPayload(quota)
}

func quotaObservationPayload(quota coreauth.QuotaState) gin.H {
	observed := gin.H{}
	if !quota.ObservedAt.IsZero() {
		observed["observed_at"] = quota.ObservedAt
	}
	signals := make(map[string]string, len(quota.Signals))
	for key, value := range quota.Signals {
		signals[key] = value
	}
	observed["signals"] = signals
	return observed
}

func modelQuotaObservationPayload(provider string, states map[string]*coreauth.ModelState) gin.H {
	if !coreauth.ProviderSupportsQuotaObservation(provider) {
		return gin.H{}
	}
	observations := gin.H{}
	for model, state := range states {
		if state == nil {
			continue
		}
		if state.Quota.ObservedAt.IsZero() && len(state.Quota.Signals) == 0 {
			continue
		}
		observations[model] = quotaObservationPayloadForProvider(provider, state.Quota)
	}
	return observations
}

func authWeightValue(auth *coreauth.Auth) (int64, bool) {
	if auth == nil {
		return 0, false
	}
	if rawWeight := strings.TrimSpace(authAttribute(auth, coreauth.AttributeWeight)); rawWeight != "" {
		weight, errWeight := credentialweight.ParseString(rawWeight)
		return weight, errWeight == nil
	}
	if auth.Metadata == nil {
		return 0, false
	}
	rawWeight, ok := auth.Metadata[coreauth.AttributeWeight]
	if !ok || rawWeight == nil {
		return 0, false
	}
	weight, errWeight := credentialweight.ParseValue(rawWeight)
	return weight, errWeight == nil
}

func authWebsocketsValue(auth *coreauth.Auth) (bool, bool) {
	if auth == nil {
		return false, false
	}
	if auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed, true
			}
		}
	}
	if auth.Metadata == nil {
		return false, false
	}
	raw, ok := auth.Metadata["websockets"]
	if !ok || raw == nil {
		return false, false
	}
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, false
}

func authProjectID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["project_id"].(string); ok {
			if projectID := strings.TrimSpace(v); projectID != "" {
				return projectID
			}
		}
	}
	if auth.Attributes != nil {
		if projectID := strings.TrimSpace(auth.Attributes["project_id"]); projectID != "" {
			return projectID
		}
	}
	return ""
}

// RefreshOllamaBalance handles a POST request to refresh the ollama balance for a specific auth entry.
// It invalidates the cache and fetches fresh data from ollama.com/settings.
func (h *Handler) RefreshOllamaBalance(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	// Find auth by name or ID
	var targetAuth *coreauth.Auth
	if auth, ok := h.authManager.GetByID(name); ok {
		targetAuth = auth
	} else {
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth.FileName == name {
				targetAuth = auth
				break
			}
		}
	}

	if targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	// Only allow refresh for ollama providers (native or openai-compatibility).
	if !matchesCompatProvider(targetAuth, "ollama") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "balance refresh is only supported for ollama providers"})
		return
	}

	creds := ollamaCredentialsFromAuth(targetAuth)
	if !creds.HasAny() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no ollama credentials available; configure an api-key in openai-compatibility, run the ollama OAuth login, or add 'ollama_cookies' / 'aid' + '__Secure-session' to auth metadata"})
		return
	}

	balance, err := helps.RefreshOllamaBalanceWithCreds(creds, h.cfg, targetAuth)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to fetch ollama balance: %v", err)})
		return
	}

	resp := gin.H{
		"session_usage_pct": balance.SessionUsagePct,
		"weekly_usage_pct":  balance.WeeklyUsagePct,
		"session_resets_at": balance.SessionResetsAt,
		"weekly_resets_at":  balance.WeeklyResetsAt,
		"plan":              balance.Plan,
		"fetched_at":        balance.FetchedAt,
	}
	if balance.Source != "" {
		resp["source"] = balance.Source
	}
	c.JSON(http.StatusOK, gin.H{"balance": resp})
}

// deepseekCredentialsFromAuth assembles credentials usable to call
// platform.deepseek.com/api/v0/users/get_user_summary.
//
// The balance Bearer is intentionally separate from the inference api_key:
// users mint a dedicated platform Bearer token from platform.deepseek.com and
// configure it as `balance-token` on the openai-compatibility api-key entry,
// or `balance_token` / `deepseek_session_token` in auth metadata.
//
// Sources, in priority order for the Bearer token:
//  1. Attributes["balance_token"]                — synthesizer-populated from openai-compatibility `balance-token`
//  2. Attributes["deepseek_session_token"]       — explicit override
//  3. Metadata["balance_token"] / Metadata["deepseek_session_token"]
//
// Cookies (optional — used as a fallback when the platform requires WAF cookies):
//  1. Attributes["deepseek_cookies"] / Attributes["header:Cookie"]
//  2. Metadata["deepseek_cookies"]
func deepseekCredentialsFromAuth(auth *coreauth.Auth) helps.DeepSeekCredentials {
	creds := helps.DeepSeekCredentials{}
	if auth == nil {
		return creds
	}
	if auth.Attributes != nil {
		if k := strings.TrimSpace(auth.Attributes["balance_token"]); k != "" {
			creds.APIKey = k
		}
		if creds.APIKey == "" {
			if k := strings.TrimSpace(auth.Attributes["deepseek_session_token"]); k != "" {
				creds.APIKey = k
			}
		}
		if cookies := strings.TrimSpace(auth.Attributes["deepseek_cookies"]); cookies != "" {
			creds.Cookies = cookies
		}
		if creds.Cookies == "" {
			if cookies := strings.TrimSpace(auth.Attributes["header:Cookie"]); cookies != "" {
				creds.Cookies = cookies
			}
		}
	}
	if auth.Metadata != nil {
		if creds.APIKey == "" {
			if k, ok := auth.Metadata["balance_token"].(string); ok {
				if t := strings.TrimSpace(k); t != "" {
					creds.APIKey = t
				}
			}
		}
		if creds.APIKey == "" {
			if k, ok := auth.Metadata["deepseek_session_token"].(string); ok {
				if t := strings.TrimSpace(k); t != "" {
					creds.APIKey = t
				}
			}
		}
		if creds.Cookies == "" {
			if cookies, ok := auth.Metadata["deepseek_cookies"].(string); ok {
				if t := strings.TrimSpace(cookies); t != "" {
					creds.Cookies = t
				}
			}
		}
	}
	return creds
}

// deepseekBalanceToGin renders a DeepSeekBalance as a JSON-friendly map.
func deepseekBalanceToGin(balance *helps.DeepSeekBalance) gin.H {
	if balance == nil {
		return nil
	}
	b := gin.H{
		"balance":      balance.Balance,
		"monthly_cost": balance.MonthlyCost,
		"fetched_at":   balance.FetchedAt,
	}
	if balance.Currency != "" {
		b["currency"] = balance.Currency
	}
	if balance.TokenEstimation > 0 {
		b["token_estimation"] = balance.TokenEstimation
	}
	if balance.BonusBalance > 0 {
		b["bonus_balance"] = balance.BonusBalance
	}
	if balance.BonusTokenEstimation > 0 {
		b["bonus_token_estimation"] = balance.BonusTokenEstimation
	}
	if balance.TotalAvailableTokens > 0 {
		b["total_available_tokens"] = balance.TotalAvailableTokens
	}
	if balance.MonthlyTokenUsage > 0 {
		b["monthly_token_usage"] = balance.MonthlyTokenUsage
	}
	if balance.CurrentToken > 0 {
		b["current_token"] = balance.CurrentToken
	}
	if balance.Source != "" {
		b["source"] = balance.Source
	}
	return b
}

// xiaomiCredentialsFromAuth assembles credentials usable to call
// platform.xiaomimimo.com/api/v1/tokenPlan/usage.
//
// xiaomi MiMo's session cookie is bound to a single account, so the cookie
// must be supplied per api-key (under that entry's `headers.Cookie`). The
// synthesizer flattens it to auth.Attributes["header:Cookie"].
//
// Sources, in priority order:
//  1. Attributes["xiaomi_cookies"]   — explicit override
//  2. Attributes["header:Cookie"]    — synthesizer-populated from per-key headers
//  3. Metadata["xiaomi_cookies"]     — uploaded auth files / ad-hoc entries
func xiaomiCredentialsFromAuth(auth *coreauth.Auth) helps.XiaomiCredentials {
	creds := helps.XiaomiCredentials{}
	if auth == nil {
		return creds
	}
	if auth.Attributes != nil {
		if cookies := strings.TrimSpace(auth.Attributes["xiaomi_cookies"]); cookies != "" {
			creds.Cookies = cookies
		}
		if creds.Cookies == "" {
			if cookies := strings.TrimSpace(auth.Attributes["header:Cookie"]); cookies != "" {
				creds.Cookies = cookies
			}
		}
		creds.Email = strings.TrimSpace(auth.Attributes["xiaomi_email"])
		creds.Password = strings.TrimSpace(auth.Attributes["xiaomi_password"])
	}
	if creds.Cookies == "" && auth.Metadata != nil {
		if cookies, ok := auth.Metadata["xiaomi_cookies"].(string); ok {
			if t := strings.TrimSpace(cookies); t != "" {
				creds.Cookies = t
			}
		}
	}
	return creds
}

// xiaomiBalanceToGin renders a XiaomiBalance as a JSON-friendly map.
func xiaomiBalanceToGin(balance *helps.XiaomiBalance) gin.H {
	if balance == nil {
		return nil
	}
	b := gin.H{
		"month_used":         balance.MonthUsed,
		"month_limit":        balance.MonthLimit,
		"month_percent":      balance.MonthPercent,
		"plan_used":          balance.PlanUsed,
		"plan_limit":         balance.PlanLimit,
		"plan_percent":       balance.PlanPercent,
		"compensation_used":  balance.CompensationUsed,
		"compensation_limit": balance.CompensationLimit,
		"fetched_at":         balance.FetchedAt,
	}
	if balance.Source != "" {
		b["source"] = balance.Source
	}
	return b
}

// anyrouterCredentialsFromAuth assembles cookie + new-api-user credentials for
// anyrouter.top/api/user/self. Both values are tied to a single browser session
// so they belong on a per-key entry's headers (Cookie + new-api-user).
//
// Sources, in priority order:
//  1. Attributes["header:Cookie"] / Attributes["header:new-api-user"]
//     (synthesizer-populated from per-key headers)
//  2. Attributes["anyrouter_cookies"] / Attributes["anyrouter_new_api_user"]
//     (explicit overrides)
//  3. Metadata["anyrouter_cookies"] / Metadata["anyrouter_new_api_user"]
func anyrouterCredentialsFromAuth(auth *coreauth.Auth) helps.AnyrouterCredentials {
	creds := helps.AnyrouterCredentials{}
	if auth == nil {
		return creds
	}
	if auth.Attributes != nil {
		if cookies := strings.TrimSpace(auth.Attributes["anyrouter_cookies"]); cookies != "" {
			creds.Cookies = cookies
		}
		if creds.Cookies == "" {
			if cookies := strings.TrimSpace(auth.Attributes["header:Cookie"]); cookies != "" {
				creds.Cookies = cookies
			}
		}
		if user := strings.TrimSpace(auth.Attributes["anyrouter_new_api_user"]); user != "" {
			creds.NewAPIUser = user
		}
		if creds.NewAPIUser == "" {
			// Header lookup is case-insensitive in HTTP; the synthesizer stores keys
			// verbatim, so try a few common spellings users might type in YAML.
			for _, key := range []string{"header:new-api-user", "header:New-Api-User", "header:NEW-API-USER"} {
				if v := strings.TrimSpace(auth.Attributes[key]); v != "" {
					creds.NewAPIUser = v
					break
				}
			}
		}
	}
	if auth.Metadata != nil {
		if creds.Cookies == "" {
			if cookies, ok := auth.Metadata["anyrouter_cookies"].(string); ok {
				if t := strings.TrimSpace(cookies); t != "" {
					creds.Cookies = t
				}
			}
		}
		if creds.NewAPIUser == "" {
			switch v := auth.Metadata["anyrouter_new_api_user"].(type) {
			case string:
				if t := strings.TrimSpace(v); t != "" {
					creds.NewAPIUser = t
				}
			case float64:
				if v != 0 {
					creds.NewAPIUser = strconv.FormatInt(int64(v), 10)
				}
			case int:
				if v != 0 {
					creds.NewAPIUser = strconv.Itoa(v)
				}
			case int64:
				if v != 0 {
					creds.NewAPIUser = strconv.FormatInt(v, 10)
				}
			}
		}
	}
	return creds
}

// anyrouterBalanceToGin renders an AnyrouterBalance as a JSON-friendly map.
func anyrouterBalanceToGin(balance *helps.AnyrouterBalance) gin.H {
	if balance == nil {
		return nil
	}
	b := gin.H{
		"user_id":           balance.UserID,
		"username":          balance.Username,
		"display_name":      balance.DisplayName,
		"group":             balance.Group,
		"quota":             balance.Quota,
		"used_quota":        balance.UsedQuota,
		"request_count":     balance.RequestCount,
		"aff_code":          balance.AffCode,
		"aff_count":         balance.AffCount,
		"aff_quota":         balance.AffQuota,
		"aff_history_quota": balance.AffHistoryQuota,
		"fetched_at":        balance.FetchedAt,
	}
	if balance.Source != "" {
		b["source"] = balance.Source
	}
	return b
}

// RefreshDeepSeekBalance handles a POST request to refresh the deepseek balance for a specific auth entry.
// It invalidates the cache and fetches fresh data from platform.deepseek.com.
func (h *Handler) RefreshDeepSeekBalance(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	var targetAuth *coreauth.Auth
	if auth, ok := h.authManager.GetByID(name); ok {
		targetAuth = auth
	} else {
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth.FileName == name {
				targetAuth = auth
				break
			}
		}
	}

	if targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	if !matchesCompatProvider(targetAuth, "deepseek") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "balance refresh is only supported for deepseek providers"})
		return
	}

	creds := deepseekCredentialsFromAuth(targetAuth)
	if !creds.HasAny() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no deepseek balance credentials available; set 'balance-token' on the openai-compatibility api-key entry, or add 'balance_token' / 'deepseek_session_token' / 'deepseek_cookies' to auth metadata"})
		return
	}

	balance, err := helps.RefreshDeepSeekBalanceWithCreds(creds, h.cfg, targetAuth)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to fetch deepseek balance: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"balance": deepseekBalanceToGin(balance)})
}

// RefreshOpenAICompatBalance refreshes ollama/deepseek balance using
// credentials supplied in the request body, without going through the auth
// manager. Frontends that already hold the openai-compatibility entry's
// credentials can call this directly instead of resolving an auth-file name.
func (h *Handler) RefreshOpenAICompatBalance(c *gin.Context) {
	var req struct {
		Provider     string `json:"provider"`
		BaseURL      string `json:"base_url"`
		APIKey       string `json:"api_key"`
		Cookie       string `json:"cookie"`
		BalanceToken string `json:"balance_token"`
		NewAPIUser   string `json:"new_api_user"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	// Apply the per-API-key proxy_url configured on the matching openai-compatibility
	// entry, so balance lookups go through the same proxy as inference traffic. Falls
	// through to cfg.ProxyURL inside NewProxyAwareHTTPClient when no entry matches.
	proxyAuth := &coreauth.Auth{
		ProxyURL: findOpenAICompatBalanceProxyURL(
			h.cfg,
			provider,
			req.BaseURL,
			req.APIKey,
			req.Cookie,
			req.BalanceToken,
			req.NewAPIUser,
		),
	}
	switch provider {
	case "ollama":
		creds := helps.OllamaCredentials{
			APIKey:  strings.TrimSpace(req.APIKey),
			Cookies: strings.TrimSpace(req.Cookie),
		}
		if !creds.HasAny() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no ollama credentials provided; pass 'api_key' and/or 'cookie'"})
			return
		}
		balance, err := helps.RefreshOllamaBalanceWithCreds(creds, h.cfg, proxyAuth)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to fetch ollama balance: %v", err)})
			return
		}
		resp := gin.H{
			"session_usage_pct": balance.SessionUsagePct,
			"weekly_usage_pct":  balance.WeeklyUsagePct,
			"session_resets_at": balance.SessionResetsAt,
			"weekly_resets_at":  balance.WeeklyResetsAt,
			"plan":              balance.Plan,
			"fetched_at":        balance.FetchedAt,
		}
		if balance.Source != "" {
			resp["source"] = balance.Source
		}
		c.JSON(http.StatusOK, gin.H{"balance": resp})

	case "deepseek":
		creds := helps.DeepSeekCredentials{
			APIKey:  strings.TrimSpace(req.BalanceToken),
			Cookies: strings.TrimSpace(req.Cookie),
		}
		if !creds.HasAny() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no deepseek balance credentials provided; pass 'balance_token' (Bearer minted at platform.deepseek.com) and/or 'cookie'"})
			return
		}
		balance, err := helps.RefreshDeepSeekBalanceWithCreds(creds, h.cfg, proxyAuth)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to fetch deepseek balance: %v", err)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"balance": deepseekBalanceToGin(balance)})

	case "xiaomi":
		creds := helps.XiaomiCredentials{
			Cookies: strings.TrimSpace(req.Cookie),
		}
		// Try to find per-key xiaomi credentials from config
		if creds.Cookies == "" {
			xiaomiEmail, xiaomiPassword := findXiaomiCredentialsFromConfig(h.cfg, req.APIKey)
			if xiaomiEmail != "" && xiaomiPassword != "" {
				creds.Email = xiaomiEmail
				creds.Password = xiaomiPassword
			}
		}
		if !creds.HasAny() && !h.cfg.XiaomiPlatform.Enabled() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no xiaomi credentials provided; pass 'cookie' (browser session cookies for platform.xiaomimimo.com) or configure xiaomi-email/xiaomi-password in api-key-entries"})
			return
		}
		balance, err := helps.RefreshXiaomiBalanceWithCreds(creds, h.cfg, proxyAuth)
		if err != nil {
			var verReq *helps.BrowserVerificationRequired
			if errors.As(err, &verReq) {
				c.JSON(http.StatusOK, gin.H{
					"need_verification": true,
					"session_id":        verReq.SessionID,
					"email":             verReq.Email,
					"message":           verReq.Message,
				})
				return
			}
			c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to fetch xiaomi balance: %v", err)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"balance": gin.H{
			"month_used":         balance.MonthUsed,
			"month_limit":        balance.MonthLimit,
			"month_percent":      balance.MonthPercent,
			"plan_used":          balance.PlanUsed,
			"plan_limit":         balance.PlanLimit,
			"plan_percent":       balance.PlanPercent,
			"compensation_used":  balance.CompensationUsed,
			"compensation_limit": balance.CompensationLimit,
			"source":             balance.Source,
			"fetched_at":         balance.FetchedAt,
		}})

	case "anyrouter":
		creds := helps.AnyrouterCredentials{
			Cookies:    strings.TrimSpace(req.Cookie),
			NewAPIUser: strings.TrimSpace(req.NewAPIUser),
		}
		if !creds.HasAny() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no anyrouter credentials provided; pass both 'cookie' (session cookies) and 'new_api_user' (numeric user id from anyrouter.top)"})
			return
		}
		balance, err := helps.RefreshAnyrouterBalanceWithCreds(creds, h.cfg, proxyAuth)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to fetch anyrouter balance: %v", err)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"balance": gin.H{
			"user_id":           balance.UserID,
			"username":          balance.Username,
			"display_name":      balance.DisplayName,
			"group":             balance.Group,
			"quota":             balance.Quota,
			"used_quota":        balance.UsedQuota,
			"request_count":     balance.RequestCount,
			"aff_code":          balance.AffCode,
			"aff_count":         balance.AffCount,
			"aff_quota":         balance.AffQuota,
			"aff_history_quota": balance.AffHistoryQuota,
			"source":            balance.Source,
			"fetched_at":        balance.FetchedAt,
		}})

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported provider: must be 'ollama', 'deepseek', 'xiaomi', or 'anyrouter'"})
	}
}

// SubmitXiaomiVerification 向正在运行的 Playwright 浏览器 session 提交邮箱验证码。
func (h *Handler) SubmitXiaomiVerification(c *gin.Context) {
	var req struct {
		SessionID string `json:"session_id"`
		Code      string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	sessionID := strings.TrimSpace(req.SessionID)
	code := strings.TrimSpace(req.Code)
	if sessionID == "" || code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id and code are required"})
		return
	}

	if err := helps.SubmitVerificationCode(sessionID, code); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to submit verification code: %v", err)})
		return
	}

	// 等待登录完成（WaitForBrowserLogin 内部会自动缓存 cookies）
	_, err := helps.WaitForBrowserLogin(sessionID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("login after verification failed: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func extractCodexIDTokenClaims(auth *coreauth.Auth) gin.H {
	if auth == nil || auth.Metadata == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return nil
	}
	idTokenRaw, ok := auth.Metadata["id_token"].(string)
	if !ok {
		return nil
	}
	idToken := strings.TrimSpace(idTokenRaw)
	if idToken == "" {
		return nil
	}
	claims, err := codex.ParseJWTToken(idToken)
	if err != nil || claims == nil {
		return nil
	}

	result := gin.H{}
	if v := strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID); v != "" {
		result["chatgpt_account_id"] = v
	}
	if v := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType); v != "" {
		result["plan_type"] = v
	}
	if v := claims.CodexAuthInfo.ChatgptSubscriptionActiveStart; v != nil {
		result["chatgpt_subscription_active_start"] = v
	}
	if v := claims.CodexAuthInfo.ChatgptSubscriptionActiveUntil; v != nil {
		result["chatgpt_subscription_active_until"] = v
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func authEmail(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["email"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["email"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(auth.Attributes["account_email"]); v != "" {
			return v
		}
	}
	return ""
}

func authAttribute(auth *coreauth.Auth, key string) string {
	if auth == nil || len(auth.Attributes) == 0 {
		return ""
	}
	return auth.Attributes[key]
}

func isRuntimeOnlyAuth(auth *coreauth.Auth) bool {
	if auth == nil || len(auth.Attributes) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["runtime_only"]), "true")
}

func isUnsafeAuthFileName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return true
	}
	if strings.ContainsAny(name, "/\\") {
		return true
	}
	if filepath.VolumeName(name) != "" {
		return true
	}
	return false
}

// Download single auth file by name
func (h *Handler) DownloadAuthFile(c *gin.Context) {
	name := strings.TrimSpace(c.Query("name"))
	if isUnsafeAuthFileName(name) {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		c.JSON(400, gin.H{"error": "name must end with .json"})
		return
	}
	full := filepath.Join(h.cfg.AuthDir, name)
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(404, gin.H{"error": "file not found"})
		} else {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read file: %v", err)})
		}
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", name))
	c.Data(200, "application/json", data)
}

// Upload auth file: multipart or raw JSON with ?name=
func (h *Handler) UploadAuthFile(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	ctx := c.Request.Context()
	writeGuard, errGuard := authFileWriteGuardFromRequest(c)
	if errGuard != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errGuard.Error()})
		return
	}

	fileHeaders, errMultipart := h.multipartAuthFileHeaders(c)
	if errMultipart != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid multipart form: %v", errMultipart)})
		return
	}
	if len(fileHeaders) == 1 {
		if _, errUpload := h.storeUploadedAuthFile(ctx, fileHeaders[0], writeGuard); errUpload != nil {
			if errors.Is(errUpload, errAuthFileMustBeJSON) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "file must be .json"})
				return
			}
			if errors.Is(errUpload, errAuthFileWriteConflict) {
				c.JSON(http.StatusConflict, gin.H{"error": errUpload.Error()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": errUpload.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if len(fileHeaders) > 1 {
		uploaded := make([]string, 0, len(fileHeaders))
		failed := make([]gin.H, 0)
		for _, file := range fileHeaders {
			name, errUpload := h.storeUploadedAuthFile(ctx, file, writeGuard)
			if errUpload != nil {
				failureName := ""
				if file != nil {
					failureName = filepath.Base(file.Filename)
				}
				msg := errUpload.Error()
				if errors.Is(errUpload, errAuthFileMustBeJSON) {
					msg = "file must be .json"
				}
				failed = append(failed, gin.H{"name": failureName, "error": msg})
				continue
			}
			uploaded = append(uploaded, name)
		}
		if len(failed) > 0 {
			c.JSON(http.StatusMultiStatus, gin.H{
				"status":   "partial",
				"uploaded": len(uploaded),
				"files":    uploaded,
				"failed":   failed,
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "uploaded": len(uploaded), "files": uploaded})
		return
	}
	if c.ContentType() == "multipart/form-data" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no files uploaded"})
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if isUnsafeAuthFileName(name) {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		c.JSON(400, gin.H{"error": "name must end with .json"})
		return
	}
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	if err = h.writeAuthFileWithGuard(ctx, filepath.Base(name), data, writeGuard); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errAuthFileWriteConflict) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

// Delete auth files: single by name or all
func (h *Handler) DeleteAuthFile(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	ctx := c.Request.Context()
	if all := c.Query("all"); all == "true" || all == "1" || all == "*" {
		entries, err := os.ReadDir(h.cfg.AuthDir)
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read auth dir: %v", err)})
			return
		}
		deleted := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}
			full := filepath.Join(h.cfg.AuthDir, name)
			if !filepath.IsAbs(full) {
				if abs, errAbs := filepath.Abs(full); errAbs == nil {
					full = abs
				}
			}
			if err = os.Remove(full); err == nil {
				if errDel := h.deleteTokenRecord(ctx, full); errDel != nil {
					c.JSON(500, gin.H{"error": errDel.Error()})
					return
				}
				deleted++
				h.removeAuth(ctx, full)
			}
		}
		c.JSON(200, gin.H{"status": "ok", "deleted": deleted})
		return
	}

	names, errNames := requestedAuthFileNamesForDelete(c)
	if errNames != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errNames.Error()})
		return
	}
	if len(names) == 0 {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if len(names) == 1 {
		if _, status, errDelete := h.deleteAuthFileByName(ctx, names[0]); errDelete != nil {
			c.JSON(status, gin.H{"error": errDelete.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	deletedFiles := make([]string, 0, len(names))
	failed := make([]gin.H, 0)
	for _, name := range names {
		deletedName, _, errDelete := h.deleteAuthFileByName(ctx, name)
		if errDelete != nil {
			failed = append(failed, gin.H{"name": name, "error": errDelete.Error()})
			continue
		}
		deletedFiles = append(deletedFiles, deletedName)
	}
	if len(failed) > 0 {
		c.JSON(http.StatusMultiStatus, gin.H{
			"status":  "partial",
			"deleted": len(deletedFiles),
			"files":   deletedFiles,
			"failed":  failed,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "deleted": len(deletedFiles), "files": deletedFiles})
}

func (h *Handler) multipartAuthFileHeaders(c *gin.Context) ([]*multipart.FileHeader, error) {
	if h == nil || c == nil || c.ContentType() != "multipart/form-data" {
		return nil, nil
	}
	form, err := c.MultipartForm()
	if err != nil {
		return nil, err
	}
	if form == nil || len(form.File) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(form.File))
	for key := range form.File {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	headers := make([]*multipart.FileHeader, 0)
	for _, key := range keys {
		headers = append(headers, form.File[key]...)
	}
	return headers, nil
}

func (h *Handler) storeUploadedAuthFile(ctx context.Context, file *multipart.FileHeader, writeGuard *authFileWriteGuard) (string, error) {
	if file == nil {
		return "", fmt.Errorf("no file uploaded")
	}
	name := filepath.Base(strings.TrimSpace(file.Filename))
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return "", errAuthFileMustBeJSON
	}
	src, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("failed to open uploaded file: %w", err)
	}
	defer src.Close()

	data, err := io.ReadAll(src)
	if err != nil {
		return "", fmt.Errorf("failed to read uploaded file: %w", err)
	}
	if err := h.writeAuthFileWithGuard(ctx, name, data, writeGuard); err != nil {
		return "", err
	}
	return name, nil
}

func (h *Handler) writeAuthFile(ctx context.Context, name string, data []byte) error {
	return h.writeAuthFileWithGuard(ctx, name, data, nil)
}

func (h *Handler) writeAuthFileWithGuard(ctx context.Context, name string, data []byte, writeGuard *authFileWriteGuard) error {
	dst := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
	if !filepath.IsAbs(dst) {
		if abs, errAbs := filepath.Abs(dst); errAbs == nil {
			dst = abs
		}
	}

	metadata, err := decodeAuthFileMetadata(data)
	if err != nil {
		return err
	}
	if writeGuard != nil {
		if errValidate := writeGuard.validate(filepath.Base(name), dst); errValidate != nil {
			return errValidate
		}
	}
	changed := false
	if writeGuard == nil {
		if raw, errRead := os.ReadFile(dst); errRead == nil {
			if existingMetadata, errDecode := decodeAuthFileMetadata(raw); errDecode == nil {
				changed = mergeAuthFileUploadProtectedFields(metadata, &coreauth.Auth{Metadata: existingMetadata})
			}
		}
		authID := h.authIDForPath(dst)
		if authID == "" {
			authID = dst
		}
		if h != nil && h.authManager != nil {
			if existing, ok := h.authManager.GetByID(authID); ok && mergeAuthFileUploadProtectedFields(metadata, existing) {
				changed = true
			}
		}
		if h.mergeExistingAuthFileLocalFields(authID, metadata) {
			changed = true
		}
	}
	if changed {
		data, err = marshalAuthFileMetadata(metadata)
		if err != nil {
			return err
		}
	}
	auth, err := h.buildAuthFromFileMetadata(dst, data, metadata)
	if err != nil {
		return err
	}
	if errWrite := os.WriteFile(dst, data, 0o600); errWrite != nil {
		return fmt.Errorf("failed to write file: %w", errWrite)
	}
	if err := h.upsertAuthRecord(ctx, auth); err != nil {
		return err
	}
	return nil
}

func authFileWriteGuardFromRequest(c *gin.Context) (*authFileWriteGuard, error) {
	if c == nil {
		return nil, nil
	}
	encodedIdentities := strings.TrimSpace(c.GetHeader(authFileWriteIdentitiesHeader))
	expectedSHA256 := strings.ToLower(strings.TrimSpace(c.GetHeader(authFileWriteContentSHA256Header)))
	if encodedIdentities == "" && expectedSHA256 == "" {
		return nil, nil
	}
	if encodedIdentities == "" || expectedSHA256 == "" {
		return nil, fmt.Errorf("auth file write guard headers must be provided together")
	}
	decodedIdentities, errUnescape := url.QueryUnescape(encodedIdentities)
	if errUnescape != nil {
		return nil, fmt.Errorf("invalid auth file write identities")
	}
	var identities []authFileWriteIdentity
	if errDecode := json.Unmarshal([]byte(decodedIdentities), &identities); errDecode != nil || len(identities) == 0 {
		return nil, fmt.Errorf("invalid auth file write identities")
	}
	for _, identity := range identities {
		if strings.TrimSpace(identity.Name) == "" {
			return nil, fmt.Errorf("invalid auth file write identities")
		}
	}
	if len(expectedSHA256) != sha256.Size*2 {
		return nil, fmt.Errorf("invalid auth file content SHA-256")
	}
	if _, errDecode := hex.DecodeString(expectedSHA256); errDecode != nil {
		return nil, fmt.Errorf("invalid auth file content SHA-256")
	}
	return &authFileWriteGuard{expectedContentSHA256: expectedSHA256, identities: identities}, nil
}

func (g *authFileWriteGuard) validate(name, path string) error {
	if g == nil {
		return nil
	}
	for _, identity := range g.identities {
		if strings.TrimSpace(identity.Name) != name {
			return fmt.Errorf("%w: identity target mismatch", errAuthFileWriteConflict)
		}
	}
	existingData, errRead := os.ReadFile(path)
	if errRead != nil {
		return fmt.Errorf("%w: %v", errAuthFileWriteConflict, errRead)
	}
	actualSHA256 := sha256.Sum256(existingData)
	if hex.EncodeToString(actualSHA256[:]) != g.expectedContentSHA256 {
		return errAuthFileWriteConflict
	}
	return nil
}

func requestedAuthFileNamesForDelete(c *gin.Context) ([]string, error) {
	if c == nil {
		return nil, nil
	}
	names := uniqueAuthFileNames(c.QueryArray("name"))
	if len(names) > 0 {
		return names, nil
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}

	var objectBody struct {
		Name  string   `json:"name"`
		Names []string `json:"names"`
	}
	if body[0] == '[' {
		var arrayBody []string
		if err := json.Unmarshal(body, &arrayBody); err != nil {
			return nil, fmt.Errorf("invalid request body")
		}
		return uniqueAuthFileNames(arrayBody), nil
	}
	if err := json.Unmarshal(body, &objectBody); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}

	out := make([]string, 0, len(objectBody.Names)+1)
	if strings.TrimSpace(objectBody.Name) != "" {
		out = append(out, objectBody.Name)
	}
	out = append(out, objectBody.Names...)
	return uniqueAuthFileNames(out), nil
}

func uniqueAuthFileNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func (h *Handler) deleteAuthFileByName(ctx context.Context, name string) (string, int, error) {
	name = strings.TrimSpace(name)
	if isUnsafeAuthFileName(name) {
		return "", http.StatusBadRequest, fmt.Errorf("invalid name")
	}

	targetPath := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
	targetID := ""
	if targetAuth := h.findAuthForDelete(name); targetAuth != nil {
		if !isPluginVirtualSourceDelete(name, targetAuth) {
			return filepath.Base(name), http.StatusConflict, errPluginVirtualAuth
		}
		targetID = strings.TrimSpace(targetAuth.ID)
		if path := strings.TrimSpace(authAttribute(targetAuth, "path")); path != "" {
			targetPath = path
		}
	}
	if !filepath.IsAbs(targetPath) {
		if abs, errAbs := filepath.Abs(targetPath); errAbs == nil {
			targetPath = abs
		}
	}
	if errRemove := os.Remove(targetPath); errRemove != nil {
		if os.IsNotExist(errRemove) {
			return filepath.Base(name), http.StatusNotFound, errAuthFileNotFound
		}
		return filepath.Base(name), http.StatusInternalServerError, fmt.Errorf("failed to remove file: %w", errRemove)
	}
	if errDeleteRecord := h.deleteTokenRecord(ctx, targetPath); errDeleteRecord != nil {
		return filepath.Base(name), http.StatusInternalServerError, errDeleteRecord
	}
	h.removeAuthsForPath(ctx, targetPath, targetID)
	return filepath.Base(name), http.StatusOK, nil
}

func isPluginVirtualSourceDelete(name string, auth *coreauth.Auth) bool {
	if !coreauth.IsPluginVirtualAuth(auth) {
		return true
	}
	sourcePath := strings.TrimSpace(authAttribute(auth, coreauth.AttributeVirtualSource))
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(authAttribute(auth, "path"))
	}
	if sourcePath == "" {
		return false
	}
	return strings.EqualFold(filepath.Base(strings.TrimSpace(name)), filepath.Base(sourcePath))
}

func (h *Handler) findAuthForDelete(name string) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if auth, ok := h.authManager.GetByID(name); ok {
		return auth
	}
	auths := h.authManager.List()
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if strings.TrimSpace(auth.FileName) == name {
			return auth
		}
		if filepath.Base(strings.TrimSpace(authAttribute(auth, "path"))) == name {
			return auth
		}
	}
	return nil
}

func (h *Handler) authIDForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		if abs, errAbs := filepath.Abs(path); errAbs == nil {
			path = abs
		}
	}
	id := path
	if h != nil && h.cfg != nil {
		authDir := strings.TrimSpace(h.cfg.AuthDir)
		if resolvedAuthDir, errResolve := util.ResolveAuthDir(authDir); errResolve == nil && resolvedAuthDir != "" {
			authDir = resolvedAuthDir
		}
		if authDir != "" {
			authDir = filepath.Clean(authDir)
			if !filepath.IsAbs(authDir) {
				if abs, errAbs := filepath.Abs(authDir); errAbs == nil {
					authDir = abs
				}
			}
			if rel, errRel := filepath.Rel(authDir, path); errRel == nil && rel != "" {
				id = rel
			}
		}
	}
	// On Windows, normalize ID casing to avoid duplicate auth entries caused by case-insensitive paths.
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}
	return id
}

func (h *Handler) registerAuthFromFile(ctx context.Context, path string, data []byte) error {
	if h.authManager == nil {
		return nil
	}
	auth, err := h.buildAuthFromFileData(path, data)
	if err != nil {
		return err
	}
	return h.upsertAuthRecord(ctx, auth)
}

func (h *Handler) buildAuthFromFileData(path string, data []byte) (*coreauth.Auth, error) {
	if path == "" {
		return nil, fmt.Errorf("auth path is empty")
	}
	if data == nil {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read auth file: %w", err)
		}
	}
	metadata, err := decodeAuthFileMetadata(data)
	if err != nil {
		return nil, err
	}
	coreauth.NormalizeCredentialMetadata(metadata)
	authID := h.authIDForPath(path)
	if authID == "" {
		authID = path
	}
	if h.mergeExistingAuthFileLocalFields(authID, metadata) {
		data, err = marshalAuthFileMetadata(metadata)
		if err != nil {
			return nil, err
		}
	}
	return h.buildAuthFromFileMetadata(path, data, metadata)
}

func decodeAuthFileMetadata(data []byte) (map[string]any, error) {
	metadata := make(map[string]any)
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("invalid auth file: %w", err)
	}
	return metadata, nil
}

func marshalAuthFileMetadata(metadata map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(metadata); err != nil {
		return nil, fmt.Errorf("failed to encode auth file: %w", err)
	}
	return buf.Bytes(), nil
}

func (h *Handler) mergeExistingAuthFileLocalFields(authID string, metadata map[string]any) bool {
	if h == nil || h.authManager == nil || metadata == nil {
		return false
	}
	existing, ok := h.authManager.GetByID(authID)
	if !ok || existing == nil {
		return false
	}
	return mergeAuthFileLocalFields(metadata, existing)
}

func mergeAuthFileUploadProtectedFields(metadata map[string]any, existing *coreauth.Auth) bool {
	if metadata == nil || existing == nil {
		return false
	}
	changed := false
	preservedAliases := make(map[string]any, len(authFileModelAliasMetadataKeys))
	if existing.Metadata != nil {
		for _, key := range authFileModelAliasMetadataKeys {
			if value, ok := existing.Metadata[key]; ok {
				preservedAliases[key] = cloneAuthFileMetadataValue(value)
			}
		}
	}
	if len(preservedAliases) == 0 && existing.Attributes != nil {
		if raw := strings.TrimSpace(existing.Attributes["model_aliases"]); raw != "" {
			var decoded any
			if errUnmarshal := json.Unmarshal([]byte(raw), &decoded); errUnmarshal == nil {
				preservedAliases["model_aliases"] = decoded
			} else {
				preservedAliases["model_aliases"] = raw
			}
		}
	}
	if len(preservedAliases) > 0 {
		for _, key := range authFileModelAliasMetadataKeys {
			delete(metadata, key)
		}
		for key, value := range preservedAliases {
			metadata[key] = value
		}
		changed = true
	}

	proxyURL := ""
	if existing.Metadata != nil {
		if raw, ok := existing.Metadata["proxy_url"].(string); ok {
			proxyURL = strings.TrimSpace(raw)
		}
	}
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(existing.ProxyURL)
	}
	if proxyURL != "" {
		metadata["proxy_url"] = proxyURL
		changed = true
	}
	return changed
}

func mergeAuthFileLocalFields(metadata map[string]any, existing *coreauth.Auth) bool {
	if metadata == nil || existing == nil {
		return false
	}
	changed := false
	for _, key := range authFileLocalMetadataKeys {
		if _, exists := metadata[key]; exists {
			continue
		}
		if existing.Metadata != nil {
			if value, ok := existing.Metadata[key]; ok {
				metadata[key] = cloneAuthFileMetadataValue(value)
				changed = true
				continue
			}
		}
		if mergeAuthFileLocalFieldFromAttributes(metadata, existing, key) {
			changed = true
		}
	}
	return changed
}

func mergeAuthFileLocalFieldFromAttributes(metadata map[string]any, existing *coreauth.Auth, key string) bool {
	if metadata == nil || existing == nil {
		return false
	}
	switch key {
	case "priority":
		if existing.Attributes == nil {
			return false
		}
		priority := strings.TrimSpace(existing.Attributes["priority"])
		if priority == "" {
			return false
		}
		metadata[key] = priority
		return true
	case "weight":
		if existing.Attributes == nil {
			return false
		}
		weight := strings.TrimSpace(existing.Attributes["weight"])
		if weight == "" {
			return false
		}
		metadata[key] = weight
		return true
	case "note":
		if existing.Attributes == nil {
			return false
		}
		note := strings.TrimSpace(existing.Attributes["note"])
		if note == "" {
			return false
		}
		metadata[key] = note
		return true
	case "prefix":
		prefix := strings.TrimSpace(existing.Prefix)
		if prefix == "" {
			return false
		}
		metadata[key] = prefix
		return true
	case "proxy_url":
		proxyURL := strings.TrimSpace(existing.ProxyURL)
		if proxyURL == "" {
			return false
		}
		metadata[key] = proxyURL
		return true
	case "headers":
		headers := authFileHeadersFromAttributes(existing.Attributes)
		if len(headers) == 0 {
			return false
		}
		metadata[key] = headers
		return true
	case "websockets":
		if existing.Attributes == nil {
			return false
		}
		raw := strings.TrimSpace(existing.Attributes["websockets"])
		if raw == "" {
			return false
		}
		if parsed, errParse := strconv.ParseBool(raw); errParse == nil {
			metadata[key] = parsed
		} else {
			metadata[key] = raw
		}
		return true
	case "disabled":
		if !existing.Disabled && existing.Status != coreauth.StatusDisabled {
			return false
		}
		metadata[key] = true
		return true
	default:
		return false
	}
}

func authFileHeadersFromAttributes(attributes map[string]string) map[string]any {
	if len(attributes) == 0 {
		return nil
	}
	headers := make(map[string]any)
	for key, value := range attributes {
		if !strings.HasPrefix(key, "header:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(key, "header:"))
		val := strings.TrimSpace(value)
		if name == "" || val == "" {
			continue
		}
		headers[name] = val
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func cloneAuthFileMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = cloneAuthFileMetadataValue(value)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, value := range typed {
			out[key] = value
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, value := range typed {
			out[i] = cloneAuthFileMetadataValue(value)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

func authFileMetadataTouchedRoots(metadata map[string]any) map[string]struct{} {
	if len(metadata) == 0 {
		return nil
	}
	roots := make(map[string]struct{})
	for _, key := range []string{"prefix", "proxy_url", "headers", "priority", "weight", "note", "websockets", "disabled"} {
		if _, ok := metadata[key]; ok {
			roots[key] = struct{}{}
		}
	}
	return roots
}

func (h *Handler) buildAuthFromFileMetadata(path string, data []byte, metadata map[string]any) (*coreauth.Auth, error) {
	if path == "" {
		return nil, fmt.Errorf("auth path is empty")
	}
	if metadata == nil {
		return nil, fmt.Errorf("invalid auth file: empty metadata")
	}
	provider, _ := metadata["type"].(string)
	if provider == "" {
		provider = "unknown"
	}
	label := provider
	if email, ok := metadata["email"].(string); ok && email != "" {
		label = email
	}
	lastRefresh, hasLastRefresh := extractLastRefreshTimestamp(metadata)

	authID := h.authIDForPath(path)
	if authID == "" {
		authID = path
	}
	auth := (*coreauth.Auth)(nil)
	if h != nil && h.cfg != nil {
		sctx := &synthesizer.SynthesisContext{
			Config:      h.cfg,
			AuthDir:     h.cfg.AuthDir,
			Now:         time.Now(),
			IDGenerator: synthesizer.NewStableIDGenerator(),
		}
		generated, errSynthesize := synthesizer.SynthesizeAuthFile(sctx, path, data)
		if errSynthesize != nil {
			return nil, errSynthesize
		}
		if len(generated) > 0 && generated[0] != nil {
			auth = generated[0].Clone()
		}
	}
	if auth == nil {
		auth = &coreauth.Auth{
			ID:       authID,
			Provider: provider,
			Label:    label,
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path":   path,
				"source": path,
			},
			Metadata:  metadata,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
	}
	auth.ID = authID
	auth.FileName = filepath.Base(path)
	if hasLastRefresh {
		auth.LastRefreshedAt = lastRefresh
	}
	if h != nil && h.authManager != nil {
		if existing, ok := h.authManager.GetByID(authID); ok {
			auth.CreatedAt = existing.CreatedAt
			if !hasLastRefresh {
				auth.LastRefreshedAt = existing.LastRefreshedAt
			}
			auth.NextRefreshAfter = existing.NextRefreshAfter
			auth.Runtime = existing.Runtime
		}
	}
	syncAuthFileMetadataFields(auth, authFileMetadataTouchedRoots(metadata))
	coreauth.ApplyCustomHeadersFromMetadata(auth)
	return auth, nil
}

func (h *Handler) upsertAuthRecord(ctx context.Context, auth *coreauth.Auth) error {
	if h == nil || h.authManager == nil || auth == nil {
		return nil
	}
	if existing, ok := h.authManager.GetByID(auth.ID); ok {
		auth.CreatedAt = existing.CreatedAt
		_, err := h.authManager.Update(ctx, auth)
		return err
	}
	_, err := h.authManager.Register(ctx, auth)
	return err
}

// PatchAuthFileStatus toggles the disabled state of an auth file
func (h *Handler) PatchAuthFileStatus(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		Name      string `json:"name"`
		AuthIndex string `json:"auth_index"`
		Disabled  *bool  `json:"disabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	name := strings.TrimSpace(req.Name)
	authIndex := strings.TrimSpace(req.AuthIndex)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if req.Disabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "disabled is required"})
		return
	}

	ctx := c.Request.Context()

	targetAuth, _ := h.lookupAuthFile(name, authIndex)
	if targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}
	if coreauth.IsPluginVirtualAuth(targetAuth) {
		// Allow status changes only when targeting the source auth file name, matching delete semantics.
		// Expanded virtual project auths still cannot be modified independently.
		if !isPluginVirtualSourceDelete(name, targetAuth) {
			c.JSON(http.StatusConflict, gin.H{"error": errPluginVirtualAuth.Error()})
			return
		}
		if errPatch := h.patchPluginVirtualSourceStatus(ctx, targetAuth, *req.Disabled); errPatch != nil {
			status := http.StatusInternalServerError
			if errors.Is(errPatch, errAuthFileNotFound) || os.IsNotExist(errPatch) {
				status = http.StatusNotFound
			}
			c.JSON(status, gin.H{"error": errPatch.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "disabled": *req.Disabled})
		return
	}

	if coreauth.IsConfigAPIKeyAuth(targetAuth) {
		h.mu.Lock()
		handled, errToggle := toggleConfigAPIKeyExcludedAll(h.cfg, targetAuth, *req.Disabled)
		if errToggle != nil {
			h.mu.Unlock()
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update config api key: %v", errToggle)})
			return
		}
		if !handled {
			h.mu.Unlock()
			c.JSON(http.StatusNotFound, gin.H{"error": "config api key entry not found"})
			return
		}
		cfgSnapshot, okSnapshot := h.saveConfigAndSnapshotLocked(c)
		h.mu.Unlock()
		if !okSnapshot {
			return
		}
		h.reloadConfigAfterManagementSave(ctx, cfgSnapshot)
		if h.tokenStore != nil {
			_ = h.tokenStore.Delete(ctx, targetAuth.ID)
		}
		c.JSON(http.StatusOK, gin.H{
			"status":           "ok",
			"disabled":         *req.Disabled,
			"via":              "config:excluded-models",
			"excluded_pattern": configAPIKeyDisablePattern,
		})
		return
	}

	applyAuthDisabledState(targetAuth, *req.Disabled)
	if _, err := h.authManager.Update(ctx, targetAuth); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update auth: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "disabled": *req.Disabled})
}

// patchPluginVirtualSourceStatus toggles disabled on a plugin multi-auth source file and all
// runtime auths expanded from it. Virtual project children cannot be toggled independently.
func (h *Handler) patchPluginVirtualSourceStatus(ctx context.Context, targetAuth *coreauth.Auth, disabled bool) error {
	if h == nil || h.authManager == nil || targetAuth == nil {
		return fmt.Errorf("core auth manager unavailable")
	}
	sourcePath := strings.TrimSpace(authAttribute(targetAuth, coreauth.AttributeVirtualSource))
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(authAttribute(targetAuth, "path"))
	}
	if sourcePath == "" {
		return errPluginVirtualAuth
	}
	if errWrite := setSourceAuthFileDisabled(sourcePath, disabled); errWrite != nil {
		if os.IsNotExist(errWrite) {
			return errAuthFileNotFound
		}
		return fmt.Errorf("failed to update source auth file: %w", errWrite)
	}
	now := time.Now()
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if !sameAuthFilePath(authAttribute(auth, "path"), sourcePath) &&
			!sameAuthFilePath(authAttribute(auth, coreauth.AttributeVirtualSource), sourcePath) {
			continue
		}
		applyAuthDisabledState(auth, disabled)
		auth.UpdatedAt = now
		if _, errUpdate := h.authManager.Update(ctx, auth); errUpdate != nil {
			return fmt.Errorf("failed to update auth %s: %w", auth.ID, errUpdate)
		}
	}
	return nil
}

func setSourceAuthFileDisabled(path string, disabled bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("source auth path is empty")
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return errRead
	}
	metadata := make(map[string]any)
	if len(bytes.TrimSpace(data)) > 0 {
		if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
			return fmt.Errorf("invalid auth file: %w", errUnmarshal)
		}
	}
	if metadata == nil {
		metadata = make(map[string]any)
	}
	coreauth.NormalizeCredentialMetadata(metadata)
	metadata["disabled"] = disabled
	raw, errMarshal := json.Marshal(metadata)
	if errMarshal != nil {
		return fmt.Errorf("marshal auth file: %w", errMarshal)
	}
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		return errWrite
	}
	return nil
}

func applyAuthDisabledState(auth *coreauth.Auth, disabled bool) {
	if auth == nil {
		return
	}
	auth.Disabled = disabled
	if disabled {
		auth.Status = coreauth.StatusDisabled
		auth.StatusMessage = "disabled via management API"
	} else {
		auth.Status = coreauth.StatusActive
		auth.StatusMessage = ""
	}
	auth.UpdatedAt = time.Now()
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["disabled"] = disabled
}

// PatchAuthFileFields updates arbitrary metadata fields of an auth file.
func (h *Handler) PatchAuthFileFields(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req map[string]json.RawMessage
	decoder := json.NewDecoder(c.Request.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	nameRaw, ok := req["name"]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	var nameValue string
	if err := json.Unmarshal(nameRaw, &nameValue); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	name := strings.TrimSpace(nameValue)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	delete(req, "name")
	var errNormalize error
	req, errNormalize = normalizeAuthFilePatchFields(req)
	if errNormalize != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errNormalize.Error()})
		return
	}
	requestRetryPatch, errRequestRetry := decodeAuthFileRequestRetryPatch(req)
	if errRequestRetry != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errRequestRetry.Error()})
		return
	}
	delete(req, "request_retry")

	ctx := c.Request.Context()

	// Find auth by name or ID
	var targetAuth *coreauth.Auth
	if auth, ok := h.authManager.GetByID(name); ok {
		targetAuth = auth
	} else {
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth.FileName == name {
				targetAuth = auth
				break
			}
		}
	}

	if targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}
	if coreauth.IsPluginVirtualAuth(targetAuth) {
		c.JSON(http.StatusConflict, gin.H{"error": errPluginVirtualAuth.Error()})
		return
	}
	updatedAuth := targetAuth.Clone()
	if updatedAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}
	coreauth.NormalizeCredentialMetadata(updatedAuth.Metadata)

	changed := false
	touchedRoots := make(map[string]struct{}, len(req))
	for key, rawValue := range req {
		fieldPath := strings.TrimSpace(key)
		if fieldPath == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "field name is required"})
			return
		}
		value, errDecode := decodeAuthFileFieldValue(rawValue)
		if errDecode != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid field %s", fieldPath)})
			return
		}
		if updatedAuth.Metadata == nil {
			updatedAuth.Metadata = make(map[string]any)
		}
		if rootAuthFileField(fieldPath) == coreauth.AttributeWeight {
			if errWeight := validateAuthFilePatchWeightValue(value); errWeight != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": errWeight.Error()})
				return
			}
		}

		if fieldPath == "headers" {
			applyAuthFileHeadersPatch(updatedAuth, value)
		} else if errSet := setAuthFileMetadataValue(updatedAuth.Metadata, fieldPath, value); errSet != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errSet.Error()})
			return
		}
		if root := rootAuthFileField(fieldPath); root != "" {
			touchedRoots[root] = struct{}{}
		}
		changed = true
	}
	if requestRetryPatch.Set {
		if updatedAuth.Metadata == nil {
			updatedAuth.Metadata = make(map[string]any)
		}
		if requestRetryPatch.Value == nil {
			delete(updatedAuth.Metadata, "request_retry")
		} else {
			updatedAuth.Metadata["request_retry"] = *requestRetryPatch.Value
		}
		changed = true
	}
	if changed {
		syncAuthFileMetadataFields(updatedAuth, touchedRoots)
		if errWeight := coreauth.ValidateAuthWeight(updatedAuth); errWeight != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errWeight.Error()})
			return
		}
	}

	if !changed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	updatedAuth.UpdatedAt = time.Now()

	if _, err := h.authManager.Update(ctx, updatedAuth); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update auth: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func decodeAuthFileFieldValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

type authFileRequestRetryPatch struct {
	Set   bool
	Value *int
}

func normalizeAuthFilePatchFields(fields map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	normalized := make(map[string]json.RawMessage, len(fields))
	originalNames := make(map[string]string, len(fields))
	canonicalNames := make(map[string]bool, len(fields))
	for key, value := range fields {
		parts := strings.Split(strings.TrimSpace(key), ".")
		for index := range parts {
			parts[index] = strings.TrimSpace(parts[index])
		}
		originalRoot := parts[0]
		parts[0] = coreauth.CanonicalCredentialMetadataKey(originalRoot)
		canonicalPath := strings.Join(parts, ".")
		if original, exists := originalNames[canonicalPath]; exists {
			currentCanonical := originalRoot == parts[0]
			if canonicalNames[canonicalPath] != currentCanonical {
				if currentCanonical {
					normalized[canonicalPath] = value
					originalNames[canonicalPath] = key
					canonicalNames[canonicalPath] = true
				}
				continue
			}
			return nil, fmt.Errorf("auth file fields %q and %q refer to the same field", original, key)
		}
		normalized[canonicalPath] = value
		originalNames[canonicalPath] = key
		canonicalNames[canonicalPath] = originalRoot == parts[0]
	}
	return normalized, nil
}

func decodeAuthFileRequestRetryPatch(fields map[string]json.RawMessage) (authFileRequestRetryPatch, error) {
	var raw json.RawMessage
	found := false
	for key, value := range fields {
		fieldPath := strings.TrimSpace(key)
		fieldRoot := rootAuthFileField(fieldPath)
		if fieldRoot == "request_retry" && fieldPath != fieldRoot {
			return authFileRequestRetryPatch{}, fmt.Errorf("request_retry does not support nested fields")
		}
		if fieldPath == "request_retry" {
			found = true
			raw = value
		}
	}
	if !found {
		return authFileRequestRetryPatch{}, nil
	}
	value, errDecode := decodeAuthFileFieldValue(raw)
	if errDecode != nil {
		return authFileRequestRetryPatch{}, fmt.Errorf("request_retry must be an integer or null")
	}
	if value == nil {
		return authFileRequestRetryPatch{Set: true}, nil
	}
	number, okNumber := value.(json.Number)
	if !okNumber {
		return authFileRequestRetryPatch{}, fmt.Errorf("request_retry must be an integer or null")
	}
	parsed, errInt := number.Int64()
	if errInt != nil {
		return authFileRequestRetryPatch{}, fmt.Errorf("request_retry must be an integer or null")
	}
	normalized := int(parsed)
	if int64(normalized) != parsed {
		return authFileRequestRetryPatch{}, fmt.Errorf("request_retry must be an integer or null")
	}
	if normalized < 0 {
		return authFileRequestRetryPatch{Set: true}, nil
	}
	return authFileRequestRetryPatch{Set: true, Value: &normalized}, nil
}

func validateAuthFilePatchWeightValue(value any) error {
	if value == nil {
		return nil
	}
	if _, ok := value.(json.Number); !ok {
		return fmt.Errorf("weight must be a JSON integer or null")
	}
	if _, errWeight := credentialweight.ParseValue(value); errWeight != nil {
		return errWeight
	}
	return nil
}

func rootAuthFileField(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if idx := strings.Index(path, "."); idx >= 0 {
		return strings.TrimSpace(path[:idx])
	}
	return path
}

func setAuthFileMetadataValue(metadata map[string]any, path string, value any) error {
	if metadata == nil {
		return fmt.Errorf("metadata is nil")
	}
	parts := strings.Split(path, ".")
	current := metadata
	for i, rawPart := range parts {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			return fmt.Errorf("invalid field path: %s", path)
		}
		if i == len(parts)-1 {
			if value == nil {
				delete(current, part)
			} else {
				current[part] = value
			}
			return nil
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[part] = next
		}
		current = next
	}
	return nil
}

func applyAuthFileHeadersPatch(auth *coreauth.Auth, value any) {
	if auth == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	headersPatch, ok := authFileHeadersStringMap(value)
	if !ok {
		auth.Metadata["headers"] = value
		return
	}

	existingHeaders := coreauth.ExtractCustomHeadersFromMetadata(auth.Metadata)
	nextHeaders := make(map[string]string, len(existingHeaders))
	for key, val := range existingHeaders {
		nextHeaders[key] = val
	}
	for key, value := range headersPatch {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		val := strings.TrimSpace(value)
		if val == "" {
			delete(nextHeaders, name)
			continue
		}
		nextHeaders[name] = val
	}

	if len(nextHeaders) == 0 {
		delete(auth.Metadata, "headers")
		return
	}
	metaHeaders := make(map[string]any, len(nextHeaders))
	for key, value := range nextHeaders {
		metaHeaders[key] = value
	}
	auth.Metadata["headers"] = metaHeaders
}

func authFileHeadersStringMap(value any) (map[string]string, bool) {
	switch typed := value.(type) {
	case map[string]string:
		return typed, true
	case map[string]any:
		out := make(map[string]string, len(typed))
		for key, rawValue := range typed {
			value, ok := rawValue.(string)
			if !ok {
				return nil, false
			}
			out[key] = value
		}
		return out, true
	default:
		return nil, false
	}
}

func syncAuthFileMetadataFields(auth *coreauth.Auth, touchedRoots map[string]struct{}) {
	if auth == nil || len(touchedRoots) == 0 {
		return
	}
	if _, ok := touchedRoots["prefix"]; ok {
		if prefix, okString := auth.Metadata["prefix"].(string); okString {
			auth.Prefix = strings.TrimSpace(prefix)
		}
	}
	if _, ok := touchedRoots["proxy_url"]; ok {
		if proxyURL, okString := auth.Metadata["proxy_url"].(string); okString {
			auth.ProxyURL = strings.TrimSpace(proxyURL)
		}
	}
	if _, ok := touchedRoots["headers"]; ok {
		syncAuthFileHeaderAttributes(auth)
	}
	if _, ok := touchedRoots["priority"]; ok {
		syncAuthFilePriorityAttribute(auth)
	}
	if _, ok := touchedRoots["weight"]; ok {
		syncAuthFileWeightAttribute(auth)
	}
	if _, ok := touchedRoots["note"]; ok {
		syncAuthFileNoteAttribute(auth)
	}
	if _, ok := touchedRoots["websockets"]; ok {
		syncAuthFileWebsocketsAttribute(auth)
	}
	if _, ok := touchedRoots["disabled"]; ok {
		syncAuthFileDisabledState(auth)
	}
}

func syncAuthFileHeaderAttributes(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	for key := range auth.Attributes {
		if strings.HasPrefix(key, "header:") {
			delete(auth.Attributes, key)
		}
	}
	for name, value := range coreauth.ExtractCustomHeadersFromMetadata(auth.Metadata) {
		auth.Attributes["header:"+name] = value
	}
}

func syncAuthFilePriorityAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	priority, ok := authFileIntValue(auth.Metadata["priority"])
	if !ok {
		delete(auth.Attributes, "priority")
		return
	}
	if priority == 0 {
		delete(auth.Attributes, "priority")
		return
	}
	auth.Attributes["priority"] = strconv.Itoa(priority)
}

func syncAuthFileWeightAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	weight, ok := authFileIntValue(auth.Metadata["weight"])
	if !ok || weight <= 0 {
		delete(auth.Attributes, "weight")
		return
	}
	auth.Attributes["weight"] = strconv.Itoa(weight)
}

func authFileIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		if i, err := typed.Int64(); err == nil {
			return int(i), true
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return i, true
		}
	}
	return 0, false
}

func syncAuthFileNoteAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	note, ok := auth.Metadata["note"].(string)
	if !ok {
		delete(auth.Attributes, "note")
		return
	}
	note = strings.TrimSpace(note)
	if note == "" {
		delete(auth.Attributes, "note")
		return
	}
	auth.Attributes["note"] = note
}

func syncAuthFileWebsocketsAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	websockets, ok := authFileBoolValue(auth.Metadata["websockets"])
	if !ok {
		delete(auth.Attributes, "websockets")
		return
	}
	auth.Attributes["websockets"] = strconv.FormatBool(websockets)
}

func authFileBoolValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(typed))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, false
}

func syncAuthFileDisabledState(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	disabled, ok := authFileBoolValue(auth.Metadata["disabled"])
	if !ok {
		return
	}
	auth.Disabled = disabled
	if disabled {
		auth.Status = coreauth.StatusDisabled
		if strings.TrimSpace(auth.StatusMessage) == "" {
			auth.StatusMessage = "disabled via management API"
		}
		return
	}
	auth.Status = coreauth.StatusActive
	auth.StatusMessage = ""
}

func (h *Handler) removeAuth(ctx context.Context, id string) {
	if h == nil || h.authManager == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	if _, ok := h.authManager.GetByID(id); ok {
		h.authManager.Remove(ctx, id)
		return
	}
	authID := h.authIDForPath(id)
	if authID == "" {
		return
	}
	h.authManager.Remove(ctx, authID)
}

func (h *Handler) removeAuthsForPath(ctx context.Context, path string, fallbackID string) {
	if h == nil || h.authManager == nil {
		return
	}
	removed := false
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if sameAuthFilePath(authAttribute(auth, "path"), path) || sameAuthFilePath(authAttribute(auth, coreauth.AttributeVirtualSource), path) {
			h.removeAuth(ctx, auth.ID)
			removed = true
		}
	}
	if removed {
		return
	}
	if strings.TrimSpace(fallbackID) != "" {
		h.removeAuth(ctx, fallbackID)
		return
	}
	h.removeAuth(ctx, path)
}

func sameAuthFilePath(left, right string) bool {
	left = cleanAuthFilePath(left)
	right = cleanAuthFilePath(right)
	if left == "" || right == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func cleanAuthFilePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, errAbs := filepath.Abs(path); errAbs == nil && strings.TrimSpace(abs) != "" {
		path = abs
	}
	return filepath.Clean(path)
}

func (h *Handler) deleteTokenRecord(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("auth path is empty")
	}
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return fmt.Errorf("token store unavailable")
	}
	return store.Delete(ctx, path)
}

func (h *Handler) tokenStoreWithBaseDir() coreauth.Store {
	if h == nil {
		return nil
	}
	store := h.tokenStore
	if store == nil {
		store = sdkAuth.GetTokenStore()
		h.tokenStore = store
	}
	if h.cfg != nil {
		if dirSetter, ok := store.(interface{ SetBaseDir(string) }); ok {
			dirSetter.SetBaseDir(h.cfg.AuthDir)
		}
	}
	return store
}

func (h *Handler) mergeExistingAuthFileMetadata(record *coreauth.Auth) {
	if h == nil || record == nil {
		return
	}
	var existingMap map[string]any

	if h.cfg != nil && strings.TrimSpace(h.cfg.AuthDir) != "" {
		targetFile := record.FileName
		if targetFile == "" {
			targetFile = record.ID
		}
		if targetFile != "" {
			fullPath := filepath.Join(h.cfg.AuthDir, targetFile)
			if raw, errRead := os.ReadFile(fullPath); errRead == nil && len(raw) > 0 {
				_ = json.Unmarshal(raw, &existingMap)
			}
		}
	}

	if existingMap == nil && h.authManager != nil {
		if existing, ok := h.authManager.GetByID(record.ID); ok && existing != nil && existing.Metadata != nil {
			existingMap = existing.Metadata
		} else {
			for _, auth := range h.authManager.List() {
				if auth != nil && auth.FileName == record.FileName && auth.Metadata != nil {
					existingMap = auth.Metadata
					break
				}
			}
		}
	}

	if len(existingMap) > 0 {
		coreauth.MergeExistingAuthMetadata(record, existingMap)
	}
}

func (h *Handler) saveTokenRecord(ctx context.Context, record *coreauth.Auth) (string, error) {
	if record == nil {
		return "", fmt.Errorf("token record is nil")
	}
	h.mergeExistingAuthFileMetadata(record)
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return "", fmt.Errorf("token store unavailable")
	}
	if h.postAuthHook != nil {
		if err := h.postAuthHook(ctx, record); err != nil {
			return "", fmt.Errorf("post-auth hook failed: %w", err)
		}
	}
	savedPath, errSave := store.Save(ctx, record)
	if errSave != nil {
		return savedPath, errSave
	}
	if h.postAuthPersistHook != nil {
		persistedRecord := record
		if data, errRead := os.ReadFile(savedPath); errRead == nil && len(data) > 0 {
			auths, errSynthesize := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
				Config:           h.cfg,
				AuthDir:          filepath.Dir(savedPath),
				Now:              time.Now(),
				IDGenerator:      synthesizer.NewStableIDGenerator(),
				PluginAuthParser: h.pluginHost,
			}, savedPath, data)
			if errSynthesize != nil {
				return savedPath, fmt.Errorf("synthesize persisted auth failed: %w", errSynthesize)
			}
			for _, auth := range auths {
				if auth != nil && (auth.ID == record.ID || len(auths) == 1) {
					persistedRecord = auth
					break
				}
			}
		}
		if errHook := h.postAuthPersistHook(ctx, persistedRecord); errHook != nil {
			return savedPath, fmt.Errorf("post-auth persist hook failed: %w", errHook)
		}
	}
	return savedPath, nil
}

func gitLabBaseURLFromRequest(c *gin.Context) string {
	if c != nil {
		if raw := strings.TrimSpace(c.Query("base_url")); raw != "" {
			return gitlabauth.NormalizeBaseURL(raw)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("GITLAB_BASE_URL")); raw != "" {
		return gitlabauth.NormalizeBaseURL(raw)
	}
	return gitlabauth.DefaultBaseURL
}

func buildGitLabAuthMetadata(baseURL, mode string, tokenResp *gitlabauth.TokenResponse, direct *gitlabauth.DirectAccessResponse) map[string]any {
	metadata := map[string]any{
		"type":                     "gitlab",
		"auth_method":              strings.TrimSpace(mode),
		"base_url":                 gitlabauth.NormalizeBaseURL(baseURL),
		"last_refresh":             time.Now().UTC().Format(time.RFC3339),
		"refresh_interval_seconds": 240,
	}
	if tokenResp != nil {
		metadata["access_token"] = strings.TrimSpace(tokenResp.AccessToken)
		if refreshToken := strings.TrimSpace(tokenResp.RefreshToken); refreshToken != "" {
			metadata["refresh_token"] = refreshToken
		}
		if tokenType := strings.TrimSpace(tokenResp.TokenType); tokenType != "" {
			metadata["token_type"] = tokenType
		}
		if scope := strings.TrimSpace(tokenResp.Scope); scope != "" {
			metadata["scope"] = scope
		}
		if expiry := gitlabauth.TokenExpiry(time.Now(), tokenResp); !expiry.IsZero() {
			metadata["oauth_expires_at"] = expiry.Format(time.RFC3339)
		}
	}
	mergeGitLabDirectAccessMetadata(metadata, direct)
	return metadata
}

func mergeGitLabDirectAccessMetadata(metadata map[string]any, direct *gitlabauth.DirectAccessResponse) {
	if metadata == nil || direct == nil {
		return
	}
	if base := strings.TrimSpace(direct.BaseURL); base != "" {
		metadata["duo_gateway_base_url"] = base
	}
	if token := strings.TrimSpace(direct.Token); token != "" {
		metadata["duo_gateway_token"] = token
	}
	if direct.ExpiresAt > 0 {
		expiry := time.Unix(direct.ExpiresAt, 0).UTC()
		metadata["duo_gateway_expires_at"] = expiry.Format(time.RFC3339)
		now := time.Now().UTC()
		if ttl := expiry.Sub(now); ttl > 0 {
			interval := int(ttl.Seconds()) / 2
			switch {
			case interval < 60:
				interval = 60
			case interval > 240:
				interval = 240
			}
			metadata["refresh_interval_seconds"] = interval
		}
	}
	if len(direct.Headers) > 0 {
		headers := make(map[string]string, len(direct.Headers))
		for key, value := range direct.Headers {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key == "" || value == "" {
				continue
			}
			headers[key] = value
		}
		if len(headers) > 0 {
			metadata["duo_gateway_headers"] = headers
		}
	}
	if direct.ModelDetails != nil {
		modelDetails := map[string]any{}
		if provider := strings.TrimSpace(direct.ModelDetails.ModelProvider); provider != "" {
			modelDetails["model_provider"] = provider
			metadata["model_provider"] = provider
		}
		if model := strings.TrimSpace(direct.ModelDetails.ModelName); model != "" {
			modelDetails["model_name"] = model
			metadata["model_name"] = model
		}
		if len(modelDetails) > 0 {
			metadata["model_details"] = modelDetails
		}
	}
}

func primaryGitLabEmail(user *gitlabauth.User) string {
	if user == nil {
		return ""
	}
	if value := strings.TrimSpace(user.Email); value != "" {
		return value
	}
	return strings.TrimSpace(user.PublicEmail)
}

func gitLabAccountIdentifier(user *gitlabauth.User) string {
	if user == nil {
		return "user"
	}
	for _, value := range []string{user.Username, primaryGitLabEmail(user), user.Name} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return "user"
}

func sanitizeGitLabFileName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "user"
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == '.':
			builder.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				builder.WriteRune('-')
				lastDash = true
			}
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "user"
	}
	return result
}

func maskGitLabToken(token string) string {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 8 {
		return trimmed
	}
	return trimmed[:4] + "..." + trimmed[len(trimmed)-4:]
}

func (h *Handler) RequestAnthropicToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	loginProxy, okProxy := resolveLoginProxyURL(c)
	if !okProxy {
		return
	}

	fmt.Println("Initializing Claude authentication...")

	// Generate PKCE codes
	pkceCodes, err := claude.GeneratePKCECodes()
	if err != nil {
		log.Errorf("Failed to generate PKCE codes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}

	// Generate random state parameter
	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	// Initialize Claude auth service (route handshake through optional per-login proxy)
	anthropicAuth := claude.NewClaudeAuth(withLoginProxy(h.cfg, loginProxy))

	// Generate authorization URL (then override redirect_uri to reuse server port)
	authURL, state, err := anthropicAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "anthropic")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/anthropic/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute anthropic callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(anthropicCallbackPort, "anthropic", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start anthropic callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(anthropicCallbackPort, forwarder)
		}

		// Helper: wait for callback file
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-anthropic-%s.oauth", state))
		waitForFile := func(path string, timeout time.Duration) (map[string]string, error) {
			deadline := time.Now().Add(timeout)
			for {
				if !IsOAuthSessionPending(state, "anthropic") {
					return nil, errOAuthSessionNotPending
				}
				if time.Now().After(deadline) {
					SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
					return nil, fmt.Errorf("timeout waiting for OAuth callback")
				}
				data, errRead := os.ReadFile(path)
				if errRead == nil {
					var m map[string]string
					_ = json.Unmarshal(data, &m)
					_ = os.Remove(path)
					return m, nil
				}
				time.Sleep(500 * time.Millisecond)
			}
		}

		fmt.Println("Waiting for authentication callback...")
		// Wait up to 5 minutes
		resultMap, errWait := waitForFile(waitFile, 5*time.Minute)
		if errWait != nil {
			if errors.Is(errWait, errOAuthSessionNotPending) {
				return
			}
			authErr := claude.NewAuthenticationError(claude.ErrCallbackTimeout, errWait)
			log.Error(claude.GetUserFriendlyMessage(authErr))
			return
		}
		if errStr := resultMap["error"]; errStr != "" {
			oauthErr := claude.NewOAuthError(errStr, "", http.StatusBadRequest)
			log.Error(claude.GetUserFriendlyMessage(oauthErr))
			SetOAuthSessionError(state, "Bad request")
			return
		}
		if resultMap["state"] != state {
			authErr := claude.NewAuthenticationError(claude.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, resultMap["state"]))
			log.Error(claude.GetUserFriendlyMessage(authErr))
			SetOAuthSessionError(state, "State code error")
			return
		}

		// Parse code (Claude may append state after '#')
		rawCode := resultMap["code"]
		code := strings.Split(rawCode, "#")[0]

		// Exchange code for tokens using internal auth service
		bundle, errExchange := anthropicAuth.ExchangeCodeForTokens(ctx, code, state, pkceCodes)
		if errExchange != nil {
			authErr := claude.NewAuthenticationError(claude.ErrCodeExchangeFailed, errExchange)
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			SetOAuthSessionError(state, "Failed to exchange authorization code for tokens")
			return
		}

		// Create token storage
		tokenStorage := anthropicAuth.CreateTokenStorage(bundle)
		metadata := map[string]any{"email": tokenStorage.Email}
		if tokenStorage.AccountUUID != "" {
			metadata["account_uuid"] = tokenStorage.AccountUUID
		}
		if tokenStorage.OrganizationUUID != "" {
			metadata["organization_uuid"] = tokenStorage.OrganizationUUID
		}
		if tokenStorage.OrganizationName != "" {
			metadata["organization_name"] = tokenStorage.OrganizationName
		}
		if len(tokenStorage.DeviceIDs) > 0 {
			metadata[claude.ClaudeDeviceIDsMetadataKey] = append([]string(nil), tokenStorage.DeviceIDs...)
		}
		record := &coreauth.Auth{
			ID:       fmt.Sprintf("claude-%s.json", tokenStorage.Email),
			Provider: "claude",
			FileName: fmt.Sprintf("claude-%s.json", tokenStorage.Email),
			Storage:  tokenStorage,
			Metadata: metadata,
			ProxyURL: loginProxy,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "anthropic"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Claude services through this CLI")
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("anthropic")
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestGeminiCLIToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	loginProxy, okProxy := resolveLoginProxyURL(c)
	if !okProxy {
		return
	}
	effectiveCfg := withLoginProxy(h.cfg, loginProxy)
	proxyHTTPClient := util.SetProxy(&effectiveCfg.SDKConfig, &http.Client{})
	ctx = context.WithValue(ctx, oauth2.HTTPClient, proxyHTTPClient)

	// Optional project ID from query
	projectID := c.Query("project_id")

	fmt.Println("Initializing Google authentication...")

	// OAuth2 configuration using exported constants from internal/auth/gemini
	conf := &oauth2.Config{
		ClientID:     geminiAuth.ClientID,
		ClientSecret: geminiAuth.ClientSecret,
		RedirectURL:  fmt.Sprintf("http://localhost:%d/oauth2callback", geminiAuth.DefaultCallbackPort),
		Scopes:       geminiAuth.Scopes,
		Endpoint:     google.Endpoint,
	}

	// Build authorization URL and return it immediately
	state := fmt.Sprintf("gem-%d", time.Now().UnixNano())
	authURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))

	RegisterOAuthSession(state, "gemini")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/google/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute gemini callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(geminiCallbackPort, "gemini", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start gemini callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(geminiCallbackPort, forwarder)
		}

		// Wait for callback file written by server route
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-gemini-%s.oauth", state))
		fmt.Println("Waiting for authentication callback...")
		deadline := time.Now().Add(5 * time.Minute)
		var authCode string
		for {
			if !IsOAuthSessionPending(state, "gemini") {
				return
			}
			if time.Now().After(deadline) {
				log.Error("oauth flow timed out")
				SetOAuthSessionError(state, "OAuth flow timed out")
				return
			}
			if data, errR := os.ReadFile(waitFile); errR == nil {
				var m map[string]string
				_ = json.Unmarshal(data, &m)
				_ = os.Remove(waitFile)
				if errStr := m["error"]; errStr != "" {
					log.Errorf("Authentication failed: %s", errStr)
					SetOAuthSessionError(state, "Authentication failed")
					return
				}
				authCode = m["code"]
				if authCode == "" {
					log.Errorf("Authentication failed: code not found")
					SetOAuthSessionError(state, "Authentication failed: code not found")
					return
				}
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		// Exchange authorization code for token
		token, err := conf.Exchange(ctx, authCode)
		if err != nil {
			log.Errorf("Failed to exchange token: %v", err)
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		requestedProjectID := strings.TrimSpace(projectID)

		// Create token storage (mirrors internal/auth/gemini createTokenStorage)
		authHTTPClient := conf.Client(ctx, token)
		req, errNewRequest := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v1/userinfo?alt=json", nil)
		if errNewRequest != nil {
			log.Errorf("Could not get user info: %v", errNewRequest)
			SetOAuthSessionError(state, "Could not get user info")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

		resp, errDo := authHTTPClient.Do(req)
		if errDo != nil {
			log.Errorf("Failed to execute request: %v", errDo)
			SetOAuthSessionError(state, "Failed to execute request")
			return
		}
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Printf("warn: failed to close response body: %v", errClose)
			}
		}()

		bodyBytes, _ := io.ReadAll(resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Errorf("Get user info request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
			SetOAuthSessionError(state, fmt.Sprintf("Get user info request failed with status %d", resp.StatusCode))
			return
		}

		email := gjson.GetBytes(bodyBytes, "email").String()
		if email != "" {
			fmt.Printf("Authenticated user email: %s\n", email)
		} else {
			fmt.Println("Failed to get user email from token")
		}

		// Marshal/unmarshal oauth2.Token to generic map and enrich fields
		var ifToken map[string]any
		jsonData, _ := json.Marshal(token)
		if errUnmarshal := json.Unmarshal(jsonData, &ifToken); errUnmarshal != nil {
			log.Errorf("Failed to unmarshal token: %v", errUnmarshal)
			SetOAuthSessionError(state, "Failed to unmarshal token")
			return
		}

		ifToken["token_uri"] = "https://oauth2.googleapis.com/token"
		ifToken["client_id"] = geminiAuth.ClientID
		ifToken["client_secret"] = geminiAuth.ClientSecret
		ifToken["scopes"] = geminiAuth.Scopes
		ifToken["universe_domain"] = "googleapis.com"

		ts := geminiAuth.GeminiTokenStorage{
			Token:     ifToken,
			ProjectID: requestedProjectID,
			Email:     email,
			Auto:      requestedProjectID == "",
		}

		// Initialize authenticated HTTP client via GeminiAuth to honor proxy settings
		gemAuth := geminiAuth.NewGeminiAuth()
		gemClient, errGetClient := gemAuth.GetAuthenticatedClient(ctx, &ts, h.cfg, &geminiAuth.WebLoginOptions{
			NoBrowser: true,
		})
		if errGetClient != nil {
			log.Errorf("failed to get authenticated client: %v", errGetClient)
			SetOAuthSessionError(state, "Failed to get authenticated client")
			return
		}
		fmt.Println("Authentication successful.")

		if strings.EqualFold(requestedProjectID, "ALL") {
			ts.Auto = false
			projects, errAll := onboardAllGeminiProjects(ctx, gemClient, &ts)
			if errAll != nil {
				log.Errorf("Failed to complete Gemini CLI onboarding: %v", errAll)
				SetOAuthSessionError(state, fmt.Sprintf("Failed to complete Gemini CLI onboarding: %v", errAll))
				return
			}
			if errVerify := ensureGeminiProjectsEnabled(ctx, gemClient, projects); errVerify != nil {
				log.Errorf("Failed to verify Cloud AI API status: %v", errVerify)
				SetOAuthSessionError(state, fmt.Sprintf("Failed to verify Cloud AI API status: %v", errVerify))
				return
			}
			ts.ProjectID = strings.Join(projects, ",")
			ts.Checked = true
		} else if strings.EqualFold(requestedProjectID, "GOOGLE_ONE") {
			ts.Auto = false
			if errSetup := performGeminiCLISetup(ctx, gemClient, &ts, ""); errSetup != nil {
				log.Errorf("Google One auto-discovery failed: %v", errSetup)
				SetOAuthSessionError(state, fmt.Sprintf("Google One auto-discovery failed: %v", errSetup))
				return
			}
			if strings.TrimSpace(ts.ProjectID) == "" {
				log.Error("Google One auto-discovery returned empty project ID")
				SetOAuthSessionError(state, "Google One auto-discovery returned empty project ID")
				return
			}
			isChecked, errCheck := checkCloudAPIIsEnabled(ctx, gemClient, ts.ProjectID)
			if errCheck != nil {
				log.Errorf("Failed to verify Cloud AI API status: %v", errCheck)
				SetOAuthSessionError(state, fmt.Sprintf("Failed to verify Cloud AI API status: %v", errCheck))
				return
			}
			ts.Checked = isChecked
			if !isChecked {
				log.Error("Cloud AI API is not enabled for the auto-discovered project")
				SetOAuthSessionError(state, fmt.Sprintf("Cloud AI API not enabled for project %s", ts.ProjectID))
				return
			}
		} else {
			if errEnsure := ensureGeminiProjectAndOnboard(ctx, gemClient, &ts, requestedProjectID); errEnsure != nil {
				log.Errorf("Failed to complete Gemini CLI onboarding: %v", errEnsure)
				SetOAuthSessionError(state, fmt.Sprintf("Failed to complete Gemini CLI onboarding: %v", errEnsure))
				return
			}

			if strings.TrimSpace(ts.ProjectID) == "" {
				log.Error("Onboarding did not return a project ID")
				SetOAuthSessionError(state, "Failed to resolve project ID")
				return
			}

			isChecked, errCheck := checkCloudAPIIsEnabled(ctx, gemClient, ts.ProjectID)
			if errCheck != nil {
				log.Errorf("Failed to verify Cloud AI API status: %v", errCheck)
				SetOAuthSessionError(state, fmt.Sprintf("Failed to verify Cloud AI API status: %v", errCheck))
				return
			}
			ts.Checked = isChecked
			if !isChecked {
				log.Error("Cloud AI API is not enabled for the selected project")
				SetOAuthSessionError(state, fmt.Sprintf("Cloud AI API not enabled for project %s", ts.ProjectID))
				return
			}
		}

		recordMetadata := map[string]any{
			"email":      ts.Email,
			"project_id": ts.ProjectID,
			"auto":       ts.Auto,
			"checked":    ts.Checked,
		}

		fileName := geminiAuth.CredentialFileName(ts.Email, ts.ProjectID, true)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "gemini",
			FileName: fileName,
			Storage:  &ts,
			Metadata: recordMetadata,
			ProxyURL: loginProxy,
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("gemini")
		fmt.Printf("You can now use Gemini CLI services through this CLI; token saved to %s\n", savedPath)
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestCodexToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	loginProxy, okProxy := resolveLoginProxyURL(c)
	if !okProxy {
		return
	}

	fmt.Println("Initializing Codex authentication...")

	// Generate PKCE codes
	pkceCodes, err := codex.GeneratePKCECodes()
	if err != nil {
		log.Errorf("Failed to generate PKCE codes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}

	// Generate random state parameter
	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	// Initialize Codex auth service (route handshake through optional per-login proxy).
	openaiAuth := newCodexOAuthService(withLoginProxy(h.cfg, loginProxy))

	isWebUI := isWebUIRequest(c)
	redirectURI := codex.RedirectURIForPort(codex.DefaultCallbackPort)
	if isWebUI {
		redirectURI, err = h.codexRedirectURL(c)
		if err != nil {
			log.WithError(err).Error("failed to compute codex redirect URL")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to compute codex redirect url"})
			return
		}
	}

	// Generate authorization URL
	var authURL string
	if redirectService, ok := openaiAuth.(codexRedirectOAuthService); ok {
		authURL, err = redirectService.GenerateAuthURLWithRedirect(state, pkceCodes, redirectURI)
	} else {
		authURL, err = openaiAuth.GenerateAuthURL(state, pkceCodes)
	}
	if err != nil {
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "codex")

	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.codexManagementCallbackURL(c, "/codex/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute codex callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarderOn(h.codexCallbackBindHost(), codexCallbackPort, "codex", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start codex callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(codexCallbackPort, forwarder)
		}

		// Wait for callback file
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-codex-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var code string
		for {
			if !IsOAuthSessionPending(state, "codex") {
				return
			}
			if time.Now().After(deadline) {
				authErr := codex.NewAuthenticationError(codex.ErrCallbackTimeout, fmt.Errorf("timeout waiting for OAuth callback"))
				log.Error(codex.GetUserFriendlyMessage(authErr))
				SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
				return
			}
			if data, errR := os.ReadFile(waitFile); errR == nil {
				var m map[string]string
				_ = json.Unmarshal(data, &m)
				_ = os.Remove(waitFile)
				if errStr := m["error"]; errStr != "" {
					oauthErr := codex.NewOAuthError(errStr, "", http.StatusBadRequest)
					log.Error(codex.GetUserFriendlyMessage(oauthErr))
					SetOAuthSessionError(state, "Bad Request")
					return
				}
				if m["state"] != state {
					authErr := codex.NewAuthenticationError(codex.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, m["state"]))
					SetOAuthSessionError(state, "State code error")
					log.Error(codex.GetUserFriendlyMessage(authErr))
					return
				}
				code = m["code"]
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		log.Debug("Authorization code received, exchanging for tokens...")
		// Exchange code for tokens using internal auth service
		var bundle *codex.CodexAuthBundle
		var errExchange error
		if redirectService, ok := openaiAuth.(codexRedirectOAuthService); ok {
			bundle, errExchange = redirectService.ExchangeCodeForTokensWithRedirect(ctx, code, redirectURI, pkceCodes)
		} else {
			bundle, errExchange = openaiAuth.ExchangeCodeForTokens(ctx, code, pkceCodes)
		}
		if errExchange != nil {
			authErr := codex.NewAuthenticationError(codex.ErrCodeExchangeFailed, errExchange)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Failed to exchange authorization code for tokens", errExchange))
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			return
		}

		// Extract additional info for filename generation
		claims, _ := codex.ParseJWTToken(bundle.TokenData.IDToken)
		planType := ""
		hashAccountID := ""
		if claims != nil {
			planType = strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)
			if accountID := claims.GetAccountID(); accountID != "" {
				digest := sha256.Sum256([]byte(accountID))
				hashAccountID = hex.EncodeToString(digest[:])[:8]
			}
		}

		// Create token storage and persist
		tokenStorage := openaiAuth.CreateTokenStorage(bundle)
		fileName := codex.CredentialFileName(tokenStorage.Email, planType, hashAccountID, true)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "codex",
			FileName: fileName,
			Storage:  tokenStorage,
			Metadata: map[string]any{
				"email":      tokenStorage.Email,
				"account_id": tokenStorage.AccountID,
			},
			ProxyURL: loginProxy,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "codex"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			return
		}
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Codex services through this CLI")
		CompleteOAuthSession(state)
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestGitLabToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	loginProxy, okProxy := resolveLoginProxyURL(c)
	if !okProxy {
		return
	}

	fmt.Println("Initializing GitLab Duo authentication...")

	baseURL := gitLabBaseURLFromRequest(c)
	clientID := strings.TrimSpace(c.Query("client_id"))
	clientSecret := strings.TrimSpace(c.Query("client_secret"))
	if clientID == "" {
		clientID = strings.TrimSpace(os.Getenv("GITLAB_OAUTH_CLIENT_ID"))
	}
	if clientSecret == "" {
		clientSecret = strings.TrimSpace(os.Getenv("GITLAB_OAUTH_CLIENT_SECRET"))
	}
	if clientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gitlab client_id is required"})
		return
	}

	pkceCodes, err := gitlabauth.GeneratePKCECodes()
	if err != nil {
		log.Errorf("Failed to generate GitLab PKCE codes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}

	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate GitLab state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	redirectURI := gitlabauth.RedirectURL(gitlabauth.DefaultCallbackPort)
	authClient := gitlabauth.NewAuthClient(withLoginProxy(h.cfg, loginProxy))
	authURL, err := authClient.GenerateAuthURL(baseURL, clientID, redirectURI, state, pkceCodes)
	if err != nil {
		log.Errorf("Failed to generate GitLab authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "gitlab")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/gitlab/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute gitlab callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(gitlabauth.DefaultCallbackPort, "gitlab", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start gitlab callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(gitlabauth.DefaultCallbackPort, forwarder)
		}

		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-gitlab-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var code string
		for {
			if !IsOAuthSessionPending(state, "gitlab") {
				return
			}
			if time.Now().After(deadline) {
				log.Error("gitlab oauth flow timed out")
				SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
				return
			}
			if data, errRead := os.ReadFile(waitFile); errRead == nil {
				var payload map[string]string
				_ = json.Unmarshal(data, &payload)
				_ = os.Remove(waitFile)
				if errStr := strings.TrimSpace(payload["error"]); errStr != "" {
					SetOAuthSessionError(state, errStr)
					return
				}
				if payloadState := strings.TrimSpace(payload["state"]); payloadState != state {
					SetOAuthSessionError(state, "State code error")
					return
				}
				code = strings.TrimSpace(payload["code"])
				if code == "" {
					SetOAuthSessionError(state, "Authorization code missing")
					return
				}
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		tokenResp, errExchange := authClient.ExchangeCodeForTokens(ctx, baseURL, clientID, clientSecret, redirectURI, code, pkceCodes.CodeVerifier)
		if errExchange != nil {
			log.Errorf("Failed to exchange GitLab authorization code: %v", errExchange)
			SetOAuthSessionError(state, "Failed to exchange authorization code for tokens")
			return
		}

		user, errUser := authClient.GetCurrentUser(ctx, baseURL, tokenResp.AccessToken)
		if errUser != nil {
			log.Errorf("Failed to fetch GitLab user profile: %v", errUser)
			SetOAuthSessionError(state, "Failed to fetch account profile")
			return
		}

		direct, errDirect := authClient.FetchDirectAccess(ctx, baseURL, tokenResp.AccessToken)
		if errDirect != nil {
			log.Errorf("Failed to fetch GitLab direct access metadata: %v", errDirect)
			SetOAuthSessionError(state, "Failed to fetch GitLab Duo access")
			return
		}

		identifier := gitLabAccountIdentifier(user)
		fileName := fmt.Sprintf("gitlab-%s.json", sanitizeGitLabFileName(identifier))
		metadata := buildGitLabAuthMetadata(baseURL, gitLabLoginModeOAuth, tokenResp, direct)
		metadata["auth_kind"] = "oauth"
		metadata["oauth_client_id"] = clientID
		metadata["username"] = strings.TrimSpace(user.Username)
		if email := primaryGitLabEmail(user); email != "" {
			metadata["email"] = email
		}
		metadata["name"] = strings.TrimSpace(user.Name)

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "gitlab",
			FileName: fileName,
			Label:    identifier,
			Metadata: metadata,
			ProxyURL: loginProxy,
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save GitLab auth record: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("GitLab Duo authentication successful. Token saved to %s\n", savedPath)
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("gitlab")
	}()

	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestGitLabPATToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	var payload struct {
		BaseURL             string `json:"base_url"`
		PersonalAccessToken string `json:"personal_access_token"`
		Token               string `json:"token"`
		ProxyURL            string `json:"proxy_url"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid body"})
		return
	}

	loginProxy, okProxy := validateLoginProxyURL(c, payload.ProxyURL)
	if !okProxy {
		return
	}

	baseURL := gitlabauth.NormalizeBaseURL(strings.TrimSpace(payload.BaseURL))
	if baseURL == "" {
		baseURL = gitLabBaseURLFromRequest(nil)
	}
	pat := strings.TrimSpace(payload.PersonalAccessToken)
	if pat == "" {
		pat = strings.TrimSpace(payload.Token)
	}
	if pat == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "personal_access_token is required"})
		return
	}

	authClient := gitlabauth.NewAuthClient(withLoginProxy(h.cfg, loginProxy))

	user, err := authClient.GetCurrentUser(ctx, baseURL, pat)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}
	patSelf, err := authClient.GetPersonalAccessTokenSelf(ctx, baseURL, pat)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}
	direct, err := authClient.FetchDirectAccess(ctx, baseURL, pat)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}

	identifier := gitLabAccountIdentifier(user)
	fileName := fmt.Sprintf("gitlab-%s-pat.json", sanitizeGitLabFileName(identifier))
	metadata := buildGitLabAuthMetadata(baseURL, gitLabLoginModePAT, nil, direct)
	metadata["auth_kind"] = "personal_access_token"
	metadata["personal_access_token"] = pat
	metadata["token_preview"] = maskGitLabToken(pat)
	metadata["username"] = strings.TrimSpace(user.Username)
	if email := primaryGitLabEmail(user); email != "" {
		metadata["email"] = email
	}
	metadata["name"] = strings.TrimSpace(user.Name)
	if patSelf != nil {
		if name := strings.TrimSpace(patSelf.Name); name != "" {
			metadata["pat_name"] = name
		}
		if len(patSelf.Scopes) > 0 {
			metadata["pat_scopes"] = append([]string(nil), patSelf.Scopes...)
		}
	}

	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "gitlab",
		FileName: fileName,
		Label:    identifier + " (PAT)",
		Metadata: metadata,
		ProxyURL: loginProxy,
	}

	savedPath, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to save authentication tokens"})
		return
	}

	response := gin.H{
		"status":      "ok",
		"saved_path":  savedPath,
		"username":    strings.TrimSpace(user.Username),
		"email":       primaryGitLabEmail(user),
		"token_label": identifier,
	}
	if direct != nil && direct.ModelDetails != nil {
		if provider := strings.TrimSpace(direct.ModelDetails.ModelProvider); provider != "" {
			response["model_provider"] = provider
		}
		if model := strings.TrimSpace(direct.ModelDetails.ModelName); model != "" {
			response["model_name"] = model
		}
	}

	fmt.Printf("GitLab Duo PAT authentication successful. Token saved to %s\n", savedPath)
	c.JSON(http.StatusOK, response)
}

func (h *Handler) RequestAntigravityToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	loginProxy, okProxy := resolveLoginProxyURL(c)
	if !okProxy {
		return
	}

	fmt.Println("Initializing Antigravity authentication...")

	authSvc := antigravity.NewAntigravityAuth(withLoginProxy(h.cfg, loginProxy), nil)

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("Failed to generate state parameter: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", antigravity.CallbackPort)
	authURL := authSvc.BuildAuthURL(state, redirectURI)

	RegisterOAuthSession(state, "antigravity")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/antigravity/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute antigravity callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(antigravity.CallbackPort, "antigravity", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start antigravity callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(antigravity.CallbackPort, forwarder)
		}

		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-antigravity-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var authCode string
		for {
			if !IsOAuthSessionPending(state, "antigravity") {
				return
			}
			if time.Now().After(deadline) {
				log.Error("oauth flow timed out")
				SetOAuthSessionError(state, "OAuth flow timed out")
				return
			}
			if data, errReadFile := os.ReadFile(waitFile); errReadFile == nil {
				var payload map[string]string
				_ = json.Unmarshal(data, &payload)
				_ = os.Remove(waitFile)
				if errStr := strings.TrimSpace(payload["error"]); errStr != "" {
					log.Errorf("Authentication failed: %s", errStr)
					SetOAuthSessionError(state, "Authentication failed")
					return
				}
				if payloadState := strings.TrimSpace(payload["state"]); payloadState != "" && payloadState != state {
					log.Errorf("Authentication failed: state mismatch")
					SetOAuthSessionError(state, "Authentication failed: state mismatch")
					return
				}
				authCode = strings.TrimSpace(payload["code"])
				if authCode == "" {
					log.Error("Authentication failed: code not found")
					SetOAuthSessionError(state, "Authentication failed: code not found")
					return
				}
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		tokenResp, errToken := authSvc.ExchangeCodeForTokens(ctx, authCode, redirectURI)
		if errToken != nil {
			log.Errorf("Failed to exchange token: %v", errToken)
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		accessToken := strings.TrimSpace(tokenResp.AccessToken)
		if accessToken == "" {
			log.Error("antigravity: token exchange returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		email, errInfo := authSvc.FetchUserInfo(ctx, accessToken)
		if errInfo != nil {
			log.Errorf("Failed to fetch user info: %v", errInfo)
			SetOAuthSessionError(state, "Failed to fetch user info")
			return
		}
		email = strings.TrimSpace(email)
		if email == "" {
			log.Error("antigravity: user info returned empty email")
			SetOAuthSessionError(state, "Failed to fetch user info")
			return
		}

		projectID := ""
		if accessToken != "" {
			fetchedProjectID, errProject := authSvc.FetchProjectID(ctx, accessToken)
			if errProject != nil {
				log.Warnf("antigravity: failed to fetch project ID: %v", errProject)
			} else {
				projectID = fetchedProjectID
				log.Infof("antigravity: obtained project ID %s", util.HideAPIKey(projectID))
			}
		}

		now := time.Now()
		metadata := map[string]any{
			"type":          "antigravity",
			"access_token":  tokenResp.AccessToken,
			"refresh_token": tokenResp.RefreshToken,
			"expires_in":    tokenResp.ExpiresIn,
			"timestamp":     now.UnixMilli(),
			"expired":       now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
		}
		if email != "" {
			metadata["email"] = email
		}
		if projectID != "" {
			metadata["project_id"] = projectID
		}

		fileName := antigravity.CredentialFileName(email)
		label := strings.TrimSpace(email)
		if label == "" {
			label = "antigravity"
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "antigravity",
			FileName: fileName,
			Label:    label,
			Metadata: metadata,
			ProxyURL: loginProxy,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "antigravity"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if projectID != "" {
			fmt.Printf("Using GCP project: %s\n", util.HideAPIKey(projectID))
		}
		fmt.Println("You can now use Antigravity services through this CLI")
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestXAIToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	loginProxy, okProxy := resolveLoginProxyURL(c)
	if !okProxy {
		return
	}

	fmt.Println("Initializing xAI authentication...")

	state := fmt.Sprintf("xai-%d", time.Now().UnixNano())
	authSvc := xaiauth.NewXAIAuth(withLoginProxy(h.cfg, loginProxy))

	deviceFlow, errStartDeviceFlow := authSvc.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to start xAI device flow: %v", errStartDeviceFlow)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}
	authURL := strings.TrimSpace(deviceFlow.VerificationURIComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(deviceFlow.VerificationURI)
	}

	RegisterOAuthSession(state, "xai")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "xai")

		fmt.Println("Waiting for xAI authentication...")
		bundle, errWaitForAuthorization := authSvc.WaitForAuthorization(pollCtx, deviceFlow)
		if errWaitForAuthorization != nil {
			if !IsOAuthSessionPending(state, "xai") {
				return
			}
			log.Errorf("xAI authentication failed: %v", errWaitForAuthorization)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization))
			return
		}
		if !IsOAuthSessionPending(state, "xai") {
			return
		}

		tokenStorage := authSvc.CreateTokenStorage(bundle)
		if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
			log.Error("xAI token exchange returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		fileName := xaiauth.CredentialFileName(tokenStorage.Email, tokenStorage.Subject)
		label := strings.TrimSpace(tokenStorage.Email)
		if label == "" {
			label = "xAI"
		}

		metadata := map[string]any{
			"type":           "xai",
			"access_token":   tokenStorage.AccessToken,
			"refresh_token":  tokenStorage.RefreshToken,
			"id_token":       tokenStorage.IDToken,
			"token_type":     tokenStorage.TokenType,
			"expires_in":     tokenStorage.ExpiresIn,
			"expired":        tokenStorage.Expire,
			"last_refresh":   tokenStorage.LastRefresh,
			"base_url":       tokenStorage.BaseURL,
			"token_endpoint": tokenStorage.TokenEndpoint,
			"auth_kind":      "oauth",
		}
		if tokenStorage.Email != "" {
			metadata["email"] = tokenStorage.Email
		}
		if tokenStorage.Subject != "" {
			metadata["sub"] = tokenStorage.Subject
		}
		if loginProxy != "" {
			metadata["proxy_url"] = loginProxy
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "xai",
			FileName: fileName,
			Label:    label,
			Storage:  tokenStorage,
			ProxyURL: loginProxy,
			Metadata: metadata,
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"base_url":  tokenStorage.BaseURL,
			},
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "xai"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save xAI token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use xAI services through this CLI")
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	} else {
		response["expires_in"] = int(xaiauth.MaxPollDuration / time.Second)
	}
	c.JSON(200, response)
}

func (h *Handler) RequestKimiToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	loginProxy, okProxy := resolveLoginProxyURL(c)
	if !okProxy {
		return
	}

	fmt.Println("Initializing Kimi authentication...")

	state := fmt.Sprintf("kmi-%d", time.Now().UnixNano())
	// Initialize Kimi auth service
	kimiAuth := kimi.NewKimiAuth(withLoginProxy(h.cfg, loginProxy))

	// Generate authorization URL
	deviceFlow, errStartDeviceFlow := kimiAuth.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to generate authorization URL: %v", errStartDeviceFlow)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}
	authURL := deviceFlow.VerificationURIComplete
	if authURL == "" {
		authURL = deviceFlow.VerificationURI
	}

	RegisterOAuthSession(state, "kimi")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "kimi")

		fmt.Println("Waiting for authentication...")
		authBundle, errWaitForAuthorization := kimiAuth.WaitForAuthorization(pollCtx, deviceFlow)
		if errWaitForAuthorization != nil {
			if !IsOAuthSessionPending(state, "kimi") {
				return
			}
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization))
			fmt.Printf("Authentication failed: %v\n", errWaitForAuthorization)
			return
		}
		if !IsOAuthSessionPending(state, "kimi") {
			return
		}

		// Create token storage
		tokenStorage := kimiAuth.CreateTokenStorage(authBundle)

		metadata := map[string]any{
			"type":          "kimi",
			"access_token":  authBundle.TokenData.AccessToken,
			"refresh_token": authBundle.TokenData.RefreshToken,
			"token_type":    authBundle.TokenData.TokenType,
			"scope":         authBundle.TokenData.Scope,
			"timestamp":     time.Now().UnixMilli(),
		}
		if authBundle.TokenData.ExpiresAt > 0 {
			expired := time.Unix(authBundle.TokenData.ExpiresAt, 0).UTC().Format(time.RFC3339)
			metadata["expired"] = expired
		}
		if strings.TrimSpace(authBundle.DeviceID) != "" {
			metadata["device_id"] = strings.TrimSpace(authBundle.DeviceID)
		}

		fileName := fmt.Sprintf("kimi-%d.json", time.Now().UnixMilli())
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kimi",
			FileName: fileName,
			Label:    "Kimi User",
			Storage:  tokenStorage,
			Metadata: metadata,
			ProxyURL: loginProxy,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "kimi"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Kimi services through this CLI")
		CompleteOAuthSession(state)
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	}
	c.JSON(200, response)
}

func (h *Handler) RequestIFlowToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	loginProxy, okProxy := resolveLoginProxyURL(c)
	if !okProxy {
		return
	}

	fmt.Println("Initializing iFlow authentication...")

	state := fmt.Sprintf("ifl-%d", time.Now().UnixNano())
	authSvc := iflowauth.NewIFlowAuth(withLoginProxy(h.cfg, loginProxy))
	authURL, redirectURI := authSvc.AuthorizationURL(state, iflowauth.CallbackPort)

	RegisterOAuthSession(state, "iflow")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/iflow/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute iflow callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(iflowauth.CallbackPort, "iflow", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start iflow callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(iflowauth.CallbackPort, forwarder)
		}
		fmt.Println("Waiting for authentication...")

		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-iflow-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var resultMap map[string]string
		for {
			if !IsOAuthSessionPending(state, "iflow") {
				return
			}
			if time.Now().After(deadline) {
				SetOAuthSessionError(state, "Authentication failed")
				fmt.Println("Authentication failed: timeout waiting for callback")
				return
			}
			if data, errR := os.ReadFile(waitFile); errR == nil {
				_ = os.Remove(waitFile)
				_ = json.Unmarshal(data, &resultMap)
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		if errStr := strings.TrimSpace(resultMap["error"]); errStr != "" {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Printf("Authentication failed: %s\n", errStr)
			return
		}
		if resultState := strings.TrimSpace(resultMap["state"]); resultState != state {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Println("Authentication failed: state mismatch")
			return
		}

		code := strings.TrimSpace(resultMap["code"])
		if code == "" {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Println("Authentication failed: code missing")
			return
		}

		tokenData, errExchange := authSvc.ExchangeCodeForTokens(ctx, code, redirectURI)
		if errExchange != nil {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Printf("Authentication failed: %v\n", errExchange)
			return
		}

		tokenStorage := authSvc.CreateTokenStorage(tokenData)
		identifier := strings.TrimSpace(tokenStorage.Email)
		if identifier == "" {
			identifier = fmt.Sprintf("%d", time.Now().UnixMilli())
			tokenStorage.Email = identifier
		}
		record := &coreauth.Auth{
			ID:         fmt.Sprintf("iflow-%s.json", identifier),
			Provider:   "iflow",
			FileName:   fmt.Sprintf("iflow-%s.json", identifier),
			Storage:    tokenStorage,
			Metadata:   map[string]any{"email": identifier, "api_key": tokenStorage.APIKey},
			Attributes: map[string]string{"api_key": tokenStorage.APIKey},
			ProxyURL:   loginProxy,
		}

		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if tokenStorage.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use iFlow services through this CLI")
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("iflow")
	}()

	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestGitHubToken(c *gin.Context) {
	ctx := context.Background()

	loginProxy, okProxy := resolveLoginProxyURL(c)
	if !okProxy {
		return
	}

	fmt.Println("Initializing GitHub Copilot authentication...")

	state := fmt.Sprintf("gh-%d", time.Now().UnixNano())

	// Initialize Copilot auth service
	deviceClient := copilot.NewDeviceFlowClient(withLoginProxy(h.cfg, loginProxy))

	// Initiate device flow
	deviceCode, err := deviceClient.RequestDeviceCode(ctx)
	if err != nil {
		log.Errorf("Failed to initiate device flow: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initiate device flow"})
		return
	}

	authURL := deviceCode.VerificationURI
	userCode := deviceCode.UserCode

	RegisterOAuthSession(state, "github-copilot")

	go func() {
		fmt.Printf("Please visit %s and enter code: %s\n", authURL, userCode)

		tokenData, errPoll := deviceClient.PollForToken(ctx, deviceCode)
		if errPoll != nil {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Printf("Authentication failed: %v\n", errPoll)
			return
		}

		userInfo, errUser := deviceClient.FetchUserInfo(ctx, tokenData.AccessToken)
		if errUser != nil {
			log.Warnf("Failed to fetch user info: %v", errUser)
		}

		username := userInfo.Login
		if username == "" {
			username = "github-user"
		}

		tokenStorage := &copilot.CopilotTokenStorage{
			AccessToken: tokenData.AccessToken,
			TokenType:   tokenData.TokenType,
			Scope:       tokenData.Scope,
			Username:    username,
			Email:       userInfo.Email,
			Name:        userInfo.Name,
			Type:        "github-copilot",
		}

		fileName := fmt.Sprintf("github-copilot-%s.json", username)
		label := userInfo.Email
		if label == "" {
			label = username
		}
		metadata, errMeta := copilotTokenMetadata(tokenStorage)
		if errMeta != nil {
			log.Errorf("Failed to build token metadata: %v", errMeta)
			SetOAuthSessionError(state, "Failed to build token metadata")
			return
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "github-copilot",
			Label:    label,
			FileName: fileName,
			Storage:  tokenStorage,
			Metadata: metadata,
			ProxyURL: loginProxy,
		}

		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use GitHub Copilot services through this CLI")
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("github-copilot")
	}()

	c.JSON(200, gin.H{
		"status":           "ok",
		"url":              authURL,
		"state":            state,
		"user_code":        userCode,
		"verification_uri": authURL,
	})
}

func copilotTokenMetadata(storage *copilot.CopilotTokenStorage) (map[string]any, error) {
	if storage == nil {
		return nil, fmt.Errorf("token storage is nil")
	}
	payload, errMarshal := json.Marshal(storage)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal token storage: %w", errMarshal)
	}
	metadata := make(map[string]any)
	if errUnmarshal := json.Unmarshal(payload, &metadata); errUnmarshal != nil {
		return nil, fmt.Errorf("unmarshal token storage: %w", errUnmarshal)
	}
	return metadata, nil
}

func (h *Handler) RequestIFlowCookieToken(c *gin.Context) {
	ctx := context.Background()

	var payload struct {
		Cookie   string `json:"cookie"`
		ProxyURL string `json:"proxy_url"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "cookie is required"})
		return
	}

	loginProxy, okProxy := validateLoginProxyURL(c, payload.ProxyURL)
	if !okProxy {
		return
	}

	cookieValue := strings.TrimSpace(payload.Cookie)

	if cookieValue == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "cookie is required"})
		return
	}

	cookieValue, errNormalize := iflowauth.NormalizeCookie(cookieValue)
	if errNormalize != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": errNormalize.Error()})
		return
	}

	// Check for duplicate BXAuth before authentication
	bxAuth := iflowauth.ExtractBXAuth(cookieValue)
	if existingFile, err := iflowauth.CheckDuplicateBXAuth(h.cfg.AuthDir, bxAuth); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to check duplicate"})
		return
	} else if existingFile != "" {
		existingFileName := filepath.Base(existingFile)
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": "duplicate BXAuth found", "existing_file": existingFileName})
		return
	}

	authSvc := iflowauth.NewIFlowAuth(withLoginProxy(h.cfg, loginProxy))
	tokenData, errAuth := authSvc.AuthenticateWithCookie(ctx, cookieValue)
	if errAuth != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": errAuth.Error()})
		return
	}

	tokenData.Cookie = cookieValue

	tokenStorage := authSvc.CreateCookieTokenStorage(tokenData)
	email := strings.TrimSpace(tokenStorage.Email)
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "failed to extract email from token"})
		return
	}

	fileName := iflowauth.SanitizeIFlowFileName(email)
	if fileName == "" {
		fileName = fmt.Sprintf("iflow-%d", time.Now().UnixMilli())
	} else {
		fileName = fmt.Sprintf("iflow-%s", fileName)
	}

	tokenStorage.Email = email
	timestamp := time.Now().Unix()

	record := &coreauth.Auth{
		ID:       fmt.Sprintf("%s-%d.json", fileName, timestamp),
		Provider: "iflow",
		FileName: fmt.Sprintf("%s-%d.json", fileName, timestamp),
		Storage:  tokenStorage,
		Metadata: map[string]any{
			"email":        email,
			"api_key":      tokenStorage.APIKey,
			"expired":      tokenStorage.Expire,
			"cookie":       tokenStorage.Cookie,
			"type":         tokenStorage.Type,
			"last_refresh": tokenStorage.LastRefresh,
		},
		Attributes: map[string]string{
			"api_key": tokenStorage.APIKey,
		},
		ProxyURL: loginProxy,
	}

	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to save authentication tokens"})
		return
	}

	fmt.Printf("iFlow cookie authentication successful. Token saved to %s\n", savedPath)
	c.JSON(http.StatusOK, gin.H{
		"status":     "ok",
		"saved_path": savedPath,
		"email":      email,
		"expired":    tokenStorage.Expire,
		"type":       tokenStorage.Type,
	})

}

type projectSelectionRequiredError struct{}

func (e *projectSelectionRequiredError) Error() string {
	return "gemini cli: project selection required"
}

func ensureGeminiProjectAndOnboard(ctx context.Context, httpClient *http.Client, storage *geminiAuth.GeminiTokenStorage, requestedProject string) error {
	if storage == nil {
		return fmt.Errorf("gemini storage is nil")
	}

	trimmedRequest := strings.TrimSpace(requestedProject)
	if trimmedRequest == "" {
		projects, errProjects := fetchGCPProjects(ctx, httpClient)
		if errProjects != nil {
			return fmt.Errorf("fetch project list: %w", errProjects)
		}
		if len(projects) == 0 {
			return fmt.Errorf("no Google Cloud projects available for this account")
		}
		trimmedRequest = strings.TrimSpace(projects[0].ProjectID)
		if trimmedRequest == "" {
			return fmt.Errorf("resolved project id is empty")
		}
		storage.Auto = true
	} else {
		storage.Auto = false
	}

	if err := performGeminiCLISetup(ctx, httpClient, storage, trimmedRequest); err != nil {
		return err
	}

	if strings.TrimSpace(storage.ProjectID) == "" {
		storage.ProjectID = trimmedRequest
	}

	return nil
}

func onboardAllGeminiProjects(ctx context.Context, httpClient *http.Client, storage *geminiAuth.GeminiTokenStorage) ([]string, error) {
	projects, errProjects := fetchGCPProjects(ctx, httpClient)
	if errProjects != nil {
		return nil, fmt.Errorf("fetch project list: %w", errProjects)
	}
	if len(projects) == 0 {
		return nil, fmt.Errorf("no Google Cloud projects available for this account")
	}
	activated := make([]string, 0, len(projects))
	seen := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		candidate := strings.TrimSpace(project.ProjectID)
		if candidate == "" {
			continue
		}
		if _, dup := seen[candidate]; dup {
			continue
		}
		if err := performGeminiCLISetup(ctx, httpClient, storage, candidate); err != nil {
			return nil, fmt.Errorf("onboard project %s: %w", candidate, err)
		}
		finalID := strings.TrimSpace(storage.ProjectID)
		if finalID == "" {
			finalID = candidate
		}
		activated = append(activated, finalID)
		seen[candidate] = struct{}{}
	}
	if len(activated) == 0 {
		return nil, fmt.Errorf("no Google Cloud projects available for this account")
	}
	return activated, nil
}

func ensureGeminiProjectsEnabled(ctx context.Context, httpClient *http.Client, projectIDs []string) error {
	for _, pid := range projectIDs {
		trimmed := strings.TrimSpace(pid)
		if trimmed == "" {
			continue
		}
		isChecked, errCheck := checkCloudAPIIsEnabled(ctx, httpClient, trimmed)
		if errCheck != nil {
			return fmt.Errorf("project %s: %w", trimmed, errCheck)
		}
		if !isChecked {
			return fmt.Errorf("project %s: Cloud AI API not enabled", trimmed)
		}
	}
	return nil
}

func performGeminiCLISetup(ctx context.Context, httpClient *http.Client, storage *geminiAuth.GeminiTokenStorage, requestedProject string) error {
	metadata := map[string]string{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	}

	trimmedRequest := strings.TrimSpace(requestedProject)
	explicitProject := trimmedRequest != ""

	loadReqBody := map[string]any{
		"metadata": metadata,
	}
	if explicitProject {
		loadReqBody["cloudaicompanionProject"] = trimmedRequest
	}

	var loadResp map[string]any
	if errLoad := callGeminiCLI(ctx, httpClient, "loadCodeAssist", loadReqBody, &loadResp); errLoad != nil {
		return fmt.Errorf("load code assist: %w", errLoad)
	}

	tierID := "legacy-tier"
	if tiers, okTiers := loadResp["allowedTiers"].([]any); okTiers {
		for _, rawTier := range tiers {
			tier, okTier := rawTier.(map[string]any)
			if !okTier {
				continue
			}
			if isDefault, okDefault := tier["isDefault"].(bool); okDefault && isDefault {
				if id, okID := tier["id"].(string); okID && strings.TrimSpace(id) != "" {
					tierID = strings.TrimSpace(id)
					break
				}
			}
		}
	}

	projectID := trimmedRequest
	if projectID == "" {
		if id, okProject := loadResp["cloudaicompanionProject"].(string); okProject {
			projectID = strings.TrimSpace(id)
		}
		if projectID == "" {
			if projectMap, okProject := loadResp["cloudaicompanionProject"].(map[string]any); okProject {
				if id, okID := projectMap["id"].(string); okID {
					projectID = strings.TrimSpace(id)
				}
			}
		}
	}
	if projectID == "" {
		// Auto-discovery: try onboardUser without specifying a project
		// to let Google auto-provision one (matches Gemini CLI headless behavior
		// and Antigravity's FetchProjectID pattern).
		autoOnboardReq := map[string]any{
			"tierId":   tierID,
			"metadata": metadata,
		}

		autoCtx, autoCancel := context.WithTimeout(ctx, 30*time.Second)
		defer autoCancel()
		for attempt := 1; ; attempt++ {
			var onboardResp map[string]any
			if errOnboard := callGeminiCLI(autoCtx, httpClient, "onboardUser", autoOnboardReq, &onboardResp); errOnboard != nil {
				return fmt.Errorf("auto-discovery onboardUser: %w", errOnboard)
			}

			if done, okDone := onboardResp["done"].(bool); okDone && done {
				if resp, okResp := onboardResp["response"].(map[string]any); okResp {
					switch v := resp["cloudaicompanionProject"].(type) {
					case string:
						projectID = strings.TrimSpace(v)
					case map[string]any:
						if id, okID := v["id"].(string); okID {
							projectID = strings.TrimSpace(id)
						}
					}
				}
				break
			}

			log.Debugf("Auto-discovery: onboarding in progress, attempt %d...", attempt)
			select {
			case <-autoCtx.Done():
				return &projectSelectionRequiredError{}
			case <-time.After(2 * time.Second):
			}
		}

		if projectID == "" {
			return &projectSelectionRequiredError{}
		}
		log.Infof("Auto-discovered project ID via onboarding: %s", projectID)
	}

	onboardReqBody := map[string]any{
		"tierId":                  tierID,
		"metadata":                metadata,
		"cloudaicompanionProject": projectID,
	}

	storage.ProjectID = projectID

	for {
		var onboardResp map[string]any
		if errOnboard := callGeminiCLI(ctx, httpClient, "onboardUser", onboardReqBody, &onboardResp); errOnboard != nil {
			return fmt.Errorf("onboard user: %w", errOnboard)
		}

		if done, okDone := onboardResp["done"].(bool); okDone && done {
			responseProjectID := ""
			if resp, okResp := onboardResp["response"].(map[string]any); okResp {
				switch projectValue := resp["cloudaicompanionProject"].(type) {
				case map[string]any:
					if id, okID := projectValue["id"].(string); okID {
						responseProjectID = strings.TrimSpace(id)
					}
				case string:
					responseProjectID = strings.TrimSpace(projectValue)
				}
			}

			finalProjectID := projectID
			if responseProjectID != "" {
				if explicitProject && !strings.EqualFold(responseProjectID, projectID) {
					log.Infof("Gemini onboarding: requested project %s maps to backend project %s", projectID, responseProjectID)
					log.Infof("Using backend project ID: %s", responseProjectID)
				}
				finalProjectID = responseProjectID
			}

			storage.ProjectID = strings.TrimSpace(finalProjectID)
			if storage.ProjectID == "" {
				storage.ProjectID = strings.TrimSpace(projectID)
			}
			if storage.ProjectID == "" {
				return fmt.Errorf("onboard user completed without project id")
			}
			log.Infof("Onboarding complete. Using Project ID: %s", storage.ProjectID)
			return nil
		}

		log.Println("Onboarding in progress, waiting 5 seconds...")
		time.Sleep(5 * time.Second)
	}
}

func callGeminiCLI(ctx context.Context, httpClient *http.Client, endpoint string, body any, result any) error {
	endPointURL := fmt.Sprintf("%s/%s:%s", geminiCLIEndpoint, geminiCLIVersion, endpoint)
	if strings.HasPrefix(endpoint, "operations/") {
		endPointURL = fmt.Sprintf("%s/%s", geminiCLIEndpoint, endpoint)
	}

	var reader io.Reader
	if body != nil {
		rawBody, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			return fmt.Errorf("marshal request body: %w", errMarshal)
		}
		reader = bytes.NewReader(rawBody)
	}

	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, endPointURL, reader)
	if errRequest != nil {
		return fmt.Errorf("create request: %w", errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", misc.GeminiCLIUserAgent(""))

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("api request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	if result == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	if errDecode := json.NewDecoder(resp.Body).Decode(result); errDecode != nil {
		return fmt.Errorf("decode response body: %w", errDecode)
	}

	return nil
}

func fetchGCPProjects(ctx context.Context, httpClient *http.Client) ([]interfaces.GCPProjectProjects, error) {
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloudresourcemanager.googleapis.com/v1/projects", nil)
	if errRequest != nil {
		return nil, fmt.Errorf("could not create project list request: %w", errRequest)
	}

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("failed to execute project list request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("project list request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var projects interfaces.GCPProject
	if errDecode := json.NewDecoder(resp.Body).Decode(&projects); errDecode != nil {
		return nil, fmt.Errorf("failed to unmarshal project list: %w", errDecode)
	}

	return projects.Projects, nil
}

func checkCloudAPIIsEnabled(ctx context.Context, httpClient *http.Client, projectID string) (bool, error) {
	serviceUsageURL := "https://serviceusage.googleapis.com"
	requiredServices := []string{
		"cloudaicompanion.googleapis.com",
	}
	for _, service := range requiredServices {
		checkURL := fmt.Sprintf("%s/v1/projects/%s/services/%s", serviceUsageURL, projectID, service)
		req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
		if errRequest != nil {
			return false, fmt.Errorf("failed to create request: %w", errRequest)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", misc.GeminiCLIUserAgent(""))
		resp, errDo := httpClient.Do(req)
		if errDo != nil {
			return false, fmt.Errorf("failed to execute request: %w", errDo)
		}

		if resp.StatusCode == http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			if gjson.GetBytes(bodyBytes, "state").String() == "ENABLED" {
				_ = resp.Body.Close()
				continue
			}
		}
		_ = resp.Body.Close()

		enableURL := fmt.Sprintf("%s/v1/projects/%s/services/%s:enable", serviceUsageURL, projectID, service)
		req, errRequest = http.NewRequestWithContext(ctx, http.MethodPost, enableURL, strings.NewReader("{}"))
		if errRequest != nil {
			return false, fmt.Errorf("failed to create request: %w", errRequest)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", misc.GeminiCLIUserAgent(""))
		resp, errDo = httpClient.Do(req)
		if errDo != nil {
			return false, fmt.Errorf("failed to execute request: %w", errDo)
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		errMessage := string(bodyBytes)
		errMessageResult := gjson.GetBytes(bodyBytes, "error.message")
		if errMessageResult.Exists() {
			errMessage = errMessageResult.String()
		}
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
			_ = resp.Body.Close()
			continue
		} else if resp.StatusCode == http.StatusBadRequest {
			_ = resp.Body.Close()
			if strings.Contains(strings.ToLower(errMessage), "already enabled") {
				continue
			}
		}
		_ = resp.Body.Close()
		return false, fmt.Errorf("project activation required: %s", errMessage)
	}
	return true, nil
}

// watchOAuthSessionCancel cancels pollCtx once the OAuth session is no longer pending.
func watchOAuthSessionCancel(pollCtx context.Context, cancel context.CancelFunc, state, provider string) {
	if cancel == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-pollCtx.Done():
			return
		case <-ticker.C:
			if !IsOAuthSessionPending(state, provider) {
				cancel()
				return
			}
		}
	}
}

// CancelAuthSession cancels a pending OAuth session identified by state.
// Protected by management auth. Safe for both callback and device-code flows:
// waiters check IsOAuthSessionPending and exit without saving credentials.
func (h *Handler) CancelAuthSession(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "missing state"})
		return
	}
	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}
	cancelled := CancelOAuthSession(state)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "cancelled": cancelled})
}

func (h *Handler) GetAuthStatus(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}

	provider, status, isPlugin, metadata, completed, ok := GetOAuthSessionDetails(state)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if completed {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if status != "" {
		if strings.HasPrefix(status, "device_code|") {
			parts := strings.SplitN(status, "|", 3)
			if len(parts) == 3 {
				c.JSON(http.StatusOK, gin.H{
					"status":           "device_code",
					"verification_url": parts[1],
					"user_code":        parts[2],
				})
				return
			}
		}
		if strings.HasPrefix(status, "auth_url|") {
			authURL := strings.TrimPrefix(status, "auth_url|")
			c.JSON(http.StatusOK, gin.H{
				"status": "auth_url",
				"url":    authURL,
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": status})
		return
	}
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	if isPlugin && host != nil && host.HasAuthProvider(provider) {
		ctx := PopulateAuthContext(context.Background(), c)
		resp, handled, errPoll := host.PollLogin(ctx, provider, state, metadata)
		if handled {
			if errPoll != nil {
				message := strings.TrimSpace(errPoll.Error())
				if message == "" {
					message = "Authentication failed"
				}
				SetOAuthSessionError(state, message)
				c.JSON(http.StatusOK, gin.H{"status": "error", "error": message})
				return
			}
			switch resp.Status {
			case "", pluginapi.AuthLoginStatusPending:
				c.JSON(http.StatusOK, gin.H{"status": "wait"})
				return
			case pluginapi.AuthLoginStatusError:
				message := strings.TrimSpace(resp.Message)
				if message == "" {
					message = "Authentication failed"
				}
				SetOAuthSessionError(state, message)
				c.JSON(http.StatusOK, gin.H{"status": "error", "error": message})
				return
			case pluginapi.AuthLoginStatusSuccess:
				records := pluginLoginPollAuths(host, resp)
				if len(records) == 0 {
					SetOAuthSessionError(state, "Authentication failed")
					c.JSON(http.StatusOK, gin.H{"status": "error", "error": "Authentication failed"})
					return
				}
				if errSave := h.savePluginLoginRecords(ctx, records); errSave != nil {
					log.WithError(errSave).WithField("provider", provider).Error("failed to save plugin auth tokens")
					SetOAuthSessionError(state, "Failed to save authentication tokens")
					c.JSON(http.StatusOK, gin.H{"status": "error", "error": "Failed to save authentication tokens"})
					return
				}
				CompleteOAuthSession(state)
				c.JSON(http.StatusOK, gin.H{"status": "ok"})
				return
			default:
				c.JSON(http.StatusOK, gin.H{"status": "wait"})
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "wait"})
}

func pluginLoginPollAuths(host *pluginhost.Host, resp pluginapi.AuthLoginPollResponse) []*coreauth.Auth {
	if host == nil {
		return nil
	}
	authDatas := resp.Auths
	if len(authDatas) == 0 {
		authDatas = []pluginapi.AuthData{resp.Auth}
	}
	records := make([]*coreauth.Auth, 0, len(authDatas))
	for _, authData := range authDatas {
		record := host.AuthDataToCoreAuth(authData, "", "")
		if record == nil {
			return nil
		}
		records = append(records, record)
	}
	return records
}

func (h *Handler) savePluginLoginRecords(ctx context.Context, records []*coreauth.Auth) error {
	savedPaths := make([]string, 0, len(records))
	for _, record := range records {
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if strings.TrimSpace(savedPath) != "" {
			savedPaths = append(savedPaths, savedPath)
		}
		if errSave != nil {
			h.rollbackSavedTokenRecords(ctx, savedPaths)
			return errSave
		}
	}
	return nil
}

func (h *Handler) rollbackSavedTokenRecords(ctx context.Context, savedPaths []string) {
	for i := len(savedPaths) - 1; i >= 0; i-- {
		path := strings.TrimSpace(savedPaths[i])
		if path == "" {
			continue
		}
		if errDelete := h.deleteTokenRecord(ctx, path); errDelete != nil {
			log.WithError(errDelete).WithField("path", path).Warn("failed to roll back plugin auth token")
		}
		h.removeAuthsForPath(ctx, path, path)
	}
}

// PopulateAuthContext extracts request info and adds it to the context
func PopulateAuthContext(ctx context.Context, c *gin.Context) context.Context {
	info := &coreauth.RequestInfo{
		Query:   c.Request.URL.Query(),
		Headers: c.Request.Header,
	}
	return coreauth.WithRequestInfo(ctx, info)
}

const kiroCallbackPort = 9876

func (h *Handler) RequestKiroToken(c *gin.Context) {
	ctx := context.Background()

	loginProxy, okProxy := resolveLoginProxyURL(c)
	if !okProxy {
		return
	}
	effectiveCfg := withLoginProxy(h.cfg, loginProxy)

	// Get the login method from query parameter (default: aws for device code flow)
	method := strings.ToLower(strings.TrimSpace(c.Query("method")))
	if method == "" {
		method = "aws"
	}

	fmt.Println("Initializing Kiro authentication...")

	state := fmt.Sprintf("kiro-%d", time.Now().UnixNano())

	switch method {
	case "aws", "builder-id":
		RegisterOAuthSession(state, "kiro")

		// AWS Builder ID uses device code flow (no callback needed)
		go func() {
			ssoClient := kiroauth.NewSSOOIDCClient(effectiveCfg)

			// Step 1: Register client
			fmt.Println("Registering client...")
			regResp, errRegister := ssoClient.RegisterClient(ctx)
			if errRegister != nil {
				log.Errorf("Failed to register client: %v", errRegister)
				SetOAuthSessionError(state, "Failed to register client")
				return
			}

			// Step 2: Start device authorization
			fmt.Println("Starting device authorization...")
			authResp, errAuth := ssoClient.StartDeviceAuthorization(ctx, regResp.ClientID, regResp.ClientSecret)
			if errAuth != nil {
				log.Errorf("Failed to start device auth: %v", errAuth)
				SetOAuthSessionError(state, "Failed to start device authorization")
				return
			}

			// Store the verification URL for the frontend to display.
			// Using "|" as separator because URLs contain ":".
			SetOAuthSessionError(state, "device_code|"+authResp.VerificationURIComplete+"|"+authResp.UserCode)

			// Step 3: Poll for token
			fmt.Println("Waiting for authorization...")
			interval := 5 * time.Second
			if authResp.Interval > 0 {
				interval = time.Duration(authResp.Interval) * time.Second
			}
			deadline := time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)

			for time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					SetOAuthSessionError(state, "Authorization cancelled")
					return
				case <-time.After(interval):
					tokenResp, errToken := ssoClient.CreateToken(ctx, regResp.ClientID, regResp.ClientSecret, authResp.DeviceCode)
					if errToken != nil {
						errStr := errToken.Error()
						if strings.Contains(errStr, "authorization_pending") {
							continue
						}
						if strings.Contains(errStr, "slow_down") {
							interval += 5 * time.Second
							continue
						}
						log.Errorf("Token creation failed: %v", errToken)
						SetOAuthSessionError(state, "Token creation failed")
						return
					}

					// Success! Save the token
					expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
					email := kiroauth.ExtractEmailFromJWT(tokenResp.AccessToken)

					idPart := kiroauth.SanitizeEmailForFilename(email)
					if idPart == "" {
						idPart = fmt.Sprintf("%d", time.Now().UnixNano()%100000)
					}

					now := time.Now()
					fileName := fmt.Sprintf("kiro-aws-%s.json", idPart)

					record := &coreauth.Auth{
						ID:       fileName,
						Provider: "kiro",
						FileName: fileName,
						Metadata: map[string]any{
							"type":          "kiro",
							"access_token":  tokenResp.AccessToken,
							"refresh_token": tokenResp.RefreshToken,
							"expires_at":    expiresAt.Format(time.RFC3339),
							"auth_method":   "builder-id",
							"provider":      "AWS",
							"client_id":     regResp.ClientID,
							"client_secret": regResp.ClientSecret,
							"email":         email,
							"last_refresh":  now.Format(time.RFC3339),
						},
						ProxyURL: loginProxy,
					}

					savedPath, errSave := h.saveTokenRecord(ctx, record)
					if errSave != nil {
						log.Errorf("Failed to save authentication tokens: %v", errSave)
						SetOAuthSessionError(state, "Failed to save authentication tokens")
						return
					}

					fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
					if email != "" {
						fmt.Printf("Authenticated as: %s\n", email)
					}
					CompleteOAuthSession(state)
					return
				}
			}

			SetOAuthSessionError(state, "Authorization timed out")
		}()

		// Return immediately with the state for polling
		c.JSON(http.StatusOK, gin.H{"status": "ok", "state": state, "method": "device_code"})

	case "google", "github":
		RegisterOAuthSession(state, "kiro")

		// Social auth uses protocol handler - for WEB UI we use a callback forwarder
		provider := "Google"
		if method == "github" {
			provider = "Github"
		}

		isWebUI := isWebUIRequest(c)
		var forwarder *callbackForwarder
		if isWebUI {
			targetURL, errTarget := h.managementCallbackURL("/kiro/callback")
			if errTarget != nil {
				log.WithError(errTarget).Error("failed to compute kiro callback target")
				c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
				return
			}
			var errStart error
			if forwarder, errStart = startCallbackForwarder(kiroCallbackPort, "kiro", targetURL); errStart != nil {
				log.WithError(errStart).Error("failed to start kiro callback forwarder")
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
				return
			}
		}

		go func() {
			if isWebUI {
				defer stopCallbackForwarderInstance(kiroCallbackPort, forwarder)
			}

			socialClient := kiroauth.NewSocialAuthClient(effectiveCfg)

			// Generate PKCE codes
			codeVerifier, codeChallenge, errPKCE := generateKiroPKCE()
			if errPKCE != nil {
				log.Errorf("Failed to generate PKCE: %v", errPKCE)
				SetOAuthSessionError(state, "Failed to generate PKCE")
				return
			}

			// Build login URL
			authURL := fmt.Sprintf("%s/login?idp=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=%s&prompt=select_account",
				"https://prod.us-east-1.auth.desktop.kiro.dev",
				provider,
				url.QueryEscape(kiroauth.KiroRedirectURI),
				codeChallenge,
				state,
			)

			// Store auth URL for frontend.
			// Using "|" as separator because URLs contain ":".
			SetOAuthSessionError(state, "auth_url|"+authURL)

			// Wait for callback file
			waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-kiro-%s.oauth", state))
			deadline := time.Now().Add(5 * time.Minute)

			for {
				if time.Now().After(deadline) {
					log.Error("oauth flow timed out")
					SetOAuthSessionError(state, "OAuth flow timed out")
					return
				}
				if data, errRead := os.ReadFile(waitFile); errRead == nil {
					var m map[string]string
					_ = json.Unmarshal(data, &m)
					_ = os.Remove(waitFile)
					if errStr := m["error"]; errStr != "" {
						log.Errorf("Authentication failed: %s", errStr)
						SetOAuthSessionError(state, "Authentication failed")
						return
					}
					if m["state"] != state {
						log.Errorf("State mismatch")
						SetOAuthSessionError(state, "State mismatch")
						return
					}
					code := m["code"]
					if code == "" {
						log.Error("No authorization code received")
						SetOAuthSessionError(state, "No authorization code received")
						return
					}

					// Exchange code for tokens
					tokenReq := &kiroauth.CreateTokenRequest{
						Code:         code,
						CodeVerifier: codeVerifier,
						RedirectURI:  kiroauth.KiroRedirectURI,
					}

					tokenResp, errToken := socialClient.CreateToken(ctx, tokenReq)
					if errToken != nil {
						log.Errorf("Failed to exchange code for tokens: %v", errToken)
						SetOAuthSessionError(state, "Failed to exchange code for tokens")
						return
					}

					// Save the token
					expiresIn := tokenResp.ExpiresIn
					if expiresIn <= 0 {
						expiresIn = 3600
					}
					expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
					email := kiroauth.ExtractEmailFromJWT(tokenResp.AccessToken)

					idPart := kiroauth.SanitizeEmailForFilename(email)
					if idPart == "" {
						idPart = fmt.Sprintf("%d", time.Now().UnixNano()%100000)
					}

					now := time.Now()
					fileName := fmt.Sprintf("kiro-%s-%s.json", strings.ToLower(provider), idPart)

					record := &coreauth.Auth{
						ID:       fileName,
						Provider: "kiro",
						FileName: fileName,
						Metadata: map[string]any{
							"type":          "kiro",
							"access_token":  tokenResp.AccessToken,
							"refresh_token": tokenResp.RefreshToken,
							"profile_arn":   tokenResp.ProfileArn,
							"expires_at":    expiresAt.Format(time.RFC3339),
							"auth_method":   "social",
							"provider":      provider,
							"email":         email,
							"last_refresh":  now.Format(time.RFC3339),
						},
						ProxyURL: loginProxy,
					}

					savedPath, errSave := h.saveTokenRecord(ctx, record)
					if errSave != nil {
						log.Errorf("Failed to save authentication tokens: %v", errSave)
						SetOAuthSessionError(state, "Failed to save authentication tokens")
						return
					}

					fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
					if email != "" {
						fmt.Printf("Authenticated as: %s\n", email)
					}
					CompleteOAuthSession(state)
					return
				}
				time.Sleep(500 * time.Millisecond)
			}
		}()

		c.JSON(http.StatusOK, gin.H{"status": "ok", "state": state, "method": "social"})

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid method, use 'aws', 'google', or 'github'"})
	}
}

// generateKiroPKCE generates PKCE code verifier and challenge for Kiro OAuth.
func generateKiroPKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, errRead := io.ReadFull(rand.Reader, b); errRead != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", errRead)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)

	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])

	return verifier, challenge, nil
}

func (h *Handler) RequestKiloToken(c *gin.Context) {
	ctx := context.Background()

	loginProxy, okProxy := resolveLoginProxyURL(c)
	if !okProxy {
		return
	}

	fmt.Println("Initializing Kilo authentication...")

	state := fmt.Sprintf("kil-%d", time.Now().UnixNano())
	kilocodeAuth := kilo.NewKiloAuth()

	resp, err := kilocodeAuth.InitiateDeviceFlow(ctx)
	if err != nil {
		log.Errorf("Failed to initiate device flow: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initiate device flow"})
		return
	}

	RegisterOAuthSession(state, "kilo")

	go func() {
		fmt.Printf("Please visit %s and enter code: %s\n", resp.VerificationURL, resp.Code)

		status, err := kilocodeAuth.PollForToken(ctx, resp.Code)
		if err != nil {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Printf("Authentication failed: %v\n", err)
			return
		}

		profile, err := kilocodeAuth.GetProfile(ctx, status.Token)
		if err != nil {
			log.Warnf("Failed to fetch profile: %v", err)
			profile = &kilo.Profile{Email: status.UserEmail}
		}

		var orgID string
		if len(profile.Orgs) > 0 {
			orgID = profile.Orgs[0].ID
		}

		defaults, err := kilocodeAuth.GetDefaults(ctx, status.Token, orgID)
		if err != nil {
			defaults = &kilo.Defaults{}
		}

		ts := &kilo.KiloTokenStorage{
			Token:          status.Token,
			OrganizationID: orgID,
			Model:          defaults.Model,
			Email:          status.UserEmail,
			Type:           "kilo",
		}

		fileName := kilo.CredentialFileName(status.UserEmail)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kilo",
			FileName: fileName,
			Storage:  ts,
			Metadata: map[string]any{
				"email":           status.UserEmail,
				"organization_id": orgID,
				"model":           defaults.Model,
			},
			ProxyURL: loginProxy,
		}

		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("kilo")
	}()

	c.JSON(200, gin.H{
		"status":           "ok",
		"url":              resp.VerificationURL,
		"state":            state,
		"user_code":        resp.Code,
		"verification_uri": resp.VerificationURL,
	})
}

// RequestCursorToken initiates the Cursor PKCE authentication flow.
// Supports multiple accounts via ?label=xxx query parameter.
// The user opens the returned URL in a browser, logs in, and the server polls
// until the authentication completes.
func (h *Handler) RequestCursorToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	loginProxy, okProxy := resolveLoginProxyURL(c)
	if !okProxy {
		return
	}

	label := strings.TrimSpace(c.Query("label"))
	log.Infof("Initializing Cursor authentication (label=%q)...", label)

	authParams, err := cursorauth.GenerateAuthParams()
	if err != nil {
		log.Errorf("Failed to generate Cursor auth params: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate auth params"})
		return
	}

	state := fmt.Sprintf("cur-%d", time.Now().UnixNano())
	RegisterOAuthSession(state, "cursor")

	go func() {
		log.Info("Waiting for Cursor authentication...")
		log.Infof("Open this URL in your browser: %s", authParams.LoginURL)

		tokens, errPoll := cursorauth.PollForAuth(ctx, authParams.UUID, authParams.Verifier)
		if errPoll != nil {
			SetOAuthSessionError(state, "Authentication failed: "+errPoll.Error())
			log.Errorf("Cursor authentication failed: %v", errPoll)
			return
		}

		// Build metadata
		metadata := map[string]any{
			"type":          "cursor",
			"access_token":  tokens.AccessToken,
			"refresh_token": tokens.RefreshToken,
			"timestamp":     time.Now().UnixMilli(),
		}

		// Extract expiry and account identity from JWT
		expiry := cursorauth.GetTokenExpiry(tokens.AccessToken)
		if !expiry.IsZero() {
			metadata["expires_at"] = expiry.Format(time.RFC3339)
		}

		// Auto-identify account from JWT sub claim for multi-account support
		sub := cursorauth.ParseJWTSub(tokens.AccessToken)
		subHash := cursorauth.SubToShortHash(sub)
		if sub != "" {
			metadata["sub"] = sub
		}

		fileName := cursorauth.CredentialFileName(label, subHash)
		displayLabel := cursorauth.DisplayLabel(label, subHash)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "cursor",
			FileName: fileName,
			Label:    displayLabel,
			Metadata: metadata,
			ProxyURL: loginProxy,
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save Cursor tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save tokens")
			return
		}

		log.Infof("Cursor authentication successful! Token saved to %s", savedPath)
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("cursor")
	}()

	c.JSON(200, gin.H{
		"status": "ok",
		"url":    authParams.LoginURL,
		"state":  state,
	})
}

// RefreshCodexToken handles a POST request to refresh a Codex OAuth token.
// It reads the auth file, calls the OpenAI token refresh endpoint, and saves the updated tokens.
func (h *Handler) RefreshCodexToken(c *gin.Context) {
	ctx := c.Request.Context()

	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	// Find auth by name or ID
	var targetAuth *coreauth.Auth
	if h.authManager != nil {
		if auth, ok := h.authManager.GetByID(name); ok {
			targetAuth = auth
		} else {
			auths := h.authManager.List()
			for _, auth := range auths {
				if auth.FileName == name {
					targetAuth = auth
					break
				}
			}
		}
	}

	if targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	// Only allow refresh for codex providers
	if !strings.EqualFold(targetAuth.Provider, "codex") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token refresh is only supported for codex providers"})
		return
	}

	// Resolve auth file path — prefer Attributes["path"], fallback to AuthDir + FileName
	authFilePath := strings.TrimSpace(authAttribute(targetAuth, "path"))
	if authFilePath == "" {
		baseName := strings.TrimSpace(targetAuth.FileName)
		if baseName == "" {
			baseName = targetAuth.ID
		}
		if baseName == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "cannot determine auth file path"})
			return
		}
		if filepath.IsAbs(baseName) {
			authFilePath = baseName
		} else {
			authFilePath = filepath.Join(h.cfg.AuthDir, baseName)
		}
	}

	fileData, err := os.ReadFile(authFilePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read auth file: %v", err)})
		return
	}

	var tokenStorage codex.CodexTokenStorage
	if err := json.Unmarshal(fileData, &tokenStorage); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to parse auth file: %v", err)})
		return
	}
	preservedMetadata := codexRefreshPreservedMetadata(fileData)

	refreshToken := strings.TrimSpace(tokenStorage.RefreshToken)
	if refreshToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no refresh token available in auth file"})
		return
	}

	// Create CodexAuth with proxy if configured
	proxyURL := strings.TrimSpace(targetAuth.ProxyURL)
	openaiAuth := codex.NewCodexAuthWithProxyURL(h.cfg, proxyURL)

	// Refresh the token
	tokenData, err := openaiAuth.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		log.Errorf("Failed to refresh Codex token for %s: %v", name, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to refresh token: %v", err)})
		return
	}

	// Update token storage
	openaiAuth.UpdateTokenStorage(&tokenStorage, tokenData)
	tokenStorage.SetMetadata(preservedMetadata)

	// Save updated token to file
	if err := tokenStorage.SaveTokenToFile(authFilePath); err != nil {
		log.Errorf("Failed to save refreshed token for %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to save refreshed token: %v", err)})
		return
	}

	// Update auth manager metadata
	if targetAuth.Metadata == nil {
		targetAuth.Metadata = make(map[string]any)
	}
	targetAuth.Metadata["email"] = tokenData.Email
	targetAuth.Metadata["account_id"] = tokenData.AccountID
	targetAuth.LastRefreshedAt = time.Now()

	log.Infof("Codex token refreshed successfully for %s (email=%s)", name, tokenData.Email)

	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"email":   tokenData.Email,
		"expire":  tokenData.Expire,
		"message": "token refreshed successfully",
	})
}

func codexRefreshPreservedMetadata(fileData []byte) map[string]any {
	metadata, err := decodeAuthFileMetadata(fileData)
	if err != nil || len(metadata) == 0 {
		return nil
	}
	for _, key := range []string{
		"id_token",
		"access_token",
		"refresh_token",
		"account_id",
		"last_refresh",
		"email",
		"type",
		"expired",
	} {
		delete(metadata, key)
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}
