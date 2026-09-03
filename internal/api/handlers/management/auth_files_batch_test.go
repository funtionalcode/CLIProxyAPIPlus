package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestUploadAuthFile_BatchMultipart(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	files := []struct {
		name    string
		content string
	}{
		{name: "alpha.json", content: `{"type":"codex","email":"alpha@example.com"}`},
		{name: "beta.json", content: `{"type":"claude","email":"beta@example.com"}`},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, file := range files {
		part, err := writer.CreateFormFile("file", file.name)
		if err != nil {
			t.Fatalf("failed to create multipart file: %v", err)
		}
		if _, err = part.Write([]byte(file.content)); err != nil {
			t.Fatalf("failed to write multipart content: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got, ok := payload["uploaded"].(float64); !ok || int(got) != len(files) {
		t.Fatalf("expected uploaded=%d, got %#v", len(files), payload["uploaded"])
	}

	for _, file := range files {
		fullPath := filepath.Join(authDir, file.name)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("expected uploaded file %s to exist: %v", file.name, err)
		}
		if string(data) != file.content {
			t.Fatalf("expected file %s content %q, got %q", file.name, file.content, string(data))
		}
	}

	auths := manager.List()
	if len(auths) != len(files) {
		t.Fatalf("expected %d auth entries, got %d", len(files), len(auths))
	}
}

func TestUploadAuthFile_OverwritePreservesLocalFields(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	ctx := context.Background()

	const fileName = "codex.json"
	if err := h.writeAuthFile(ctx, fileName, []byte(`{"type":"codex","email":"user@example.com"}`)); err != nil {
		t.Fatalf("failed to seed auth file: %v", err)
	}

	existing, ok := manager.GetByID(fileName)
	if !ok || existing == nil {
		t.Fatalf("expected seeded auth record")
	}
	existing.Prefix = "team"
	existing.ProxyURL = "socks5://127.0.0.1:1080"
	existing.Disabled = true
	existing.Status = coreauth.StatusDisabled
	existing.StatusMessage = "disabled by operator"
	existing.Attributes["priority"] = "9"
	existing.Attributes["note"] = "keep this account"
	existing.Attributes["websockets"] = "false"
	existing.Attributes["header:Cache-Control"] = "no-cache"
	existing.Attributes["header:Accept-Encoding"] = "identity"
	existing.Metadata["priority"] = float64(9)
	existing.Metadata["note"] = "keep this account"
	existing.Metadata["prefix"] = "team"
	existing.Metadata["proxy_url"] = "socks5://127.0.0.1:1080"
	existing.Metadata["model_aliases"] = []any{
		map[string]any{"name": "gpt-5.4", "alias": "gpt-local"},
	}
	existing.Metadata["websockets"] = false
	existing.Metadata["disabled"] = true
	existing.Metadata["tool_prefix_disabled"] = true
	existing.Metadata["request_retry"] = float64(2)
	existing.Metadata["headers"] = map[string]any{
		"Cache-Control":   "no-cache",
		"Accept-Encoding": "identity",
	}
	if _, errUpdate := manager.Update(ctx, existing); errUpdate != nil {
		t.Fatalf("failed to update existing auth: %v", errUpdate)
	}

	if err := h.writeAuthFile(ctx, fileName, []byte(`{"type":"codex","email":"user@example.com","access_token":"new-token","proxy_url":"http://incoming.proxy","model-aliases":[{"name":"gpt-incoming","alias":"incoming"}]}`)); err != nil {
		t.Fatalf("failed to overwrite auth file: %v", err)
	}

	auths := manager.List()
	if len(auths) != 1 {
		t.Fatalf("expected one auth entry after overwrite, got %d", len(auths))
	}
	updated, ok := manager.GetByID(fileName)
	if !ok || updated == nil {
		t.Fatalf("expected auth record to remain after overwrite")
	}
	if updated.Prefix != "team" {
		t.Fatalf("prefix = %q, want %q", updated.Prefix, "team")
	}
	if updated.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("proxy_url = %q, want %q", updated.ProxyURL, "socks5://127.0.0.1:1080")
	}
	if !updated.Disabled || updated.Status != coreauth.StatusDisabled {
		t.Fatalf("disabled/status = %v/%s, want true/%s", updated.Disabled, updated.Status, coreauth.StatusDisabled)
	}
	if got := updated.Attributes["priority"]; got != "9" {
		t.Fatalf("priority attr = %q, want %q", got, "9")
	}
	if got := updated.Attributes["note"]; got != "keep this account" {
		t.Fatalf("note attr = %q, want %q", got, "keep this account")
	}
	if got := updated.Attributes["websockets"]; got != "false" {
		t.Fatalf("websockets attr = %q, want %q", got, "false")
	}
	aliases := coreauth.OAuthModelAliasesFromAttributes(updated.Attributes)
	if len(aliases) != 1 || aliases[0].Name != "gpt-5.4" || aliases[0].Alias != "gpt-local" {
		t.Fatalf("model aliases = %#v, want existing alias", aliases)
	}
	if got := updated.Attributes["header:Cache-Control"]; got != "no-cache" {
		t.Fatalf("Cache-Control attr = %q, want %q", got, "no-cache")
	}
	if got := updated.Attributes["header:Accept-Encoding"]; got != "identity" {
		t.Fatalf("Accept-Encoding attr = %q, want %q", got, "identity")
	}

	raw, errRead := os.ReadFile(filepath.Join(authDir, fileName))
	if errRead != nil {
		t.Fatalf("failed to read overwritten auth file: %v", errRead)
	}
	var disk map[string]any
	if errUnmarshal := json.Unmarshal(raw, &disk); errUnmarshal != nil {
		t.Fatalf("failed to decode overwritten auth file: %v", errUnmarshal)
	}
	if got := disk["access_token"]; got != "new-token" {
		t.Fatalf("disk access_token = %#v, want %q", got, "new-token")
	}
	if got := disk["note"]; got != "keep this account" {
		t.Fatalf("disk note = %#v, want %q", got, "keep this account")
	}
	if got := disk["priority"]; got != float64(9) {
		t.Fatalf("disk priority = %#v, want 9", got)
	}
	if got := disk["tool_prefix_disabled"]; got != true {
		t.Fatalf("disk tool_prefix_disabled = %#v, want true", got)
	}
	if got := disk["request_retry"]; got != float64(2) {
		t.Fatalf("disk request_retry = %#v, want 2", got)
	}
	if got := disk["proxy_url"]; got != "socks5://127.0.0.1:1080" {
		t.Fatalf("disk proxy_url = %#v, want existing proxy", got)
	}
	diskAliases, ok := disk["model_aliases"].([]any)
	if !ok || len(diskAliases) != 1 {
		t.Fatalf("disk model_aliases = %#v, want one existing alias", disk["model_aliases"])
	}
	diskAlias, ok := diskAliases[0].(map[string]any)
	if !ok || diskAlias["name"] != "gpt-5.4" || diskAlias["alias"] != "gpt-local" {
		t.Fatalf("disk model_aliases[0] = %#v, want existing alias", diskAliases[0])
	}
	if _, exists := disk["model-aliases"]; exists {
		t.Fatalf("disk model-aliases should not keep incoming alias config")
	}
	headers, ok := disk["headers"].(map[string]any)
	if !ok {
		t.Fatalf("disk headers = %#v, want object", disk["headers"])
	}
	if got := headers["Cache-Control"]; got != "no-cache" {
		t.Fatalf("disk headers.Cache-Control = %#v, want %q", got, "no-cache")
	}
	if got := headers["Accept-Encoding"]; got != "identity" {
		t.Fatalf("disk headers.Accept-Encoding = %#v, want %q", got, "identity")
	}
}

func TestUploadAuthFile_GuardedPanelWriteCanRemoveExcludedModels(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	const fileName = "claude.json"
	existingData := []byte(`{"type":"claude","email":"user@example.com","excluded_models":["claude-fable-5","claude-fable-5-1"]}`)
	if errWrite := h.writeAuthFile(context.Background(), fileName, existingData); errWrite != nil {
		t.Fatalf("failed to seed auth file: %v", errWrite)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, errPart := writer.CreateFormFile("file", fileName)
	if errPart != nil {
		t.Fatalf("failed to create multipart file: %v", errPart)
	}
	if _, errWrite := part.Write([]byte(`{"type":"claude","email":"user@example.com"}`)); errWrite != nil {
		t.Fatalf("failed to write multipart content: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("failed to close multipart writer: %v", errClose)
	}

	expectedSHA256 := sha256.Sum256(existingData)
	identities := url.QueryEscape(`[{"name":"claude.json","runtimeId":"claude.json","provider":"claude"}]`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(authFileWriteIdentitiesHeader, identities)
	req.Header.Set(authFileWriteContentSHA256Header, hex.EncodeToString(expectedSHA256[:]))
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	raw, errRead := os.ReadFile(filepath.Join(authDir, fileName))
	if errRead != nil {
		t.Fatalf("failed to read updated auth file: %v", errRead)
	}
	var disk map[string]any
	if errDecode := json.Unmarshal(raw, &disk); errDecode != nil {
		t.Fatalf("failed to decode updated auth file: %v", errDecode)
	}
	if _, exists := disk["excluded_models"]; exists {
		t.Fatalf("excluded_models was restored after guarded panel write: %#v", disk["excluded_models"])
	}
	if _, exists := disk["excluded-models"]; exists {
		t.Fatalf("excluded-models was restored after guarded panel write: %#v", disk["excluded-models"])
	}
	updated, ok := manager.GetByID(fileName)
	if !ok || updated == nil {
		t.Fatalf("expected updated auth record")
	}
	if got := updated.Attributes["excluded_models"]; got != "" {
		t.Fatalf("excluded_models attr = %q, want empty", got)
	}
}

func TestUploadAuthFile_GuardedPanelWriteRejectsStaleContent(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	const fileName = "claude.json"
	currentData := []byte(`{"type":"claude","email":"current@example.com","excluded_models":["keep-me"]}`)
	if errWrite := h.writeAuthFile(context.Background(), fileName, currentData); errWrite != nil {
		t.Fatalf("failed to seed auth file: %v", errWrite)
	}
	staleSHA256 := sha256.Sum256([]byte(`{"type":"claude","email":"stale@example.com"}`))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, errPart := writer.CreateFormFile("file", fileName)
	if errPart != nil {
		t.Fatalf("failed to create multipart file: %v", errPart)
	}
	if _, errWrite := part.Write([]byte(`{"type":"claude","email":"replacement@example.com"}`)); errWrite != nil {
		t.Fatalf("failed to write multipart content: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("failed to close multipart writer: %v", errClose)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(authFileWriteIdentitiesHeader, url.QueryEscape(`[{"name":"claude.json"}]`))
	req.Header.Set(authFileWriteContentSHA256Header, hex.EncodeToString(staleSHA256[:]))
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("upload status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	raw, errRead := os.ReadFile(filepath.Join(authDir, fileName))
	if errRead != nil {
		t.Fatalf("failed to read auth file after conflict: %v", errRead)
	}
	if !bytes.Equal(raw, currentData) {
		t.Fatalf("auth file changed after conflict: got %s, want %s", raw, currentData)
	}
}

func TestUploadAuthFile_OverwritePreservesAliasesAndProxyFromDisk(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	const fileName = "codex.json"
	path := filepath.Join(authDir, fileName)
	existing := `{"type":"codex","access_token":"old-token","proxy_url":"socks5://127.0.0.1:1080","model-aliases":[{"name":"gpt-5.4","alias":"gpt-local"}]}`
	if errWrite := os.WriteFile(path, []byte(existing), 0o600); errWrite != nil {
		t.Fatalf("failed to seed existing auth file: %v", errWrite)
	}

	incoming := `{"type":"codex","access_token":"new-token","proxy_url":"","model_aliases":[{"name":"gpt-incoming","alias":"incoming"}]}`
	if errWrite := h.writeAuthFile(context.Background(), fileName, []byte(incoming)); errWrite != nil {
		t.Fatalf("failed to overwrite auth file: %v", errWrite)
	}

	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("failed to read overwritten auth file: %v", errRead)
	}
	var disk map[string]any
	if errUnmarshal := json.Unmarshal(raw, &disk); errUnmarshal != nil {
		t.Fatalf("failed to decode overwritten auth file: %v", errUnmarshal)
	}
	if got := disk["access_token"]; got != "new-token" {
		t.Fatalf("disk access_token = %#v, want new token", got)
	}
	if got := disk["proxy_url"]; got != "socks5://127.0.0.1:1080" {
		t.Fatalf("disk proxy_url = %#v, want existing proxy", got)
	}
	if _, exists := disk["model_aliases"]; exists {
		t.Fatalf("disk model_aliases should not keep incoming alias config")
	}
	diskAliases, ok := disk["model-aliases"].([]any)
	if !ok || len(diskAliases) != 1 {
		t.Fatalf("disk model-aliases = %#v, want one existing alias", disk["model-aliases"])
	}
	diskAlias, ok := diskAliases[0].(map[string]any)
	if !ok || diskAlias["name"] != "gpt-5.4" || diskAlias["alias"] != "gpt-local" {
		t.Fatalf("disk model-aliases[0] = %#v, want existing alias", diskAliases[0])
	}
}

func TestUploadAuthFile_BatchMultipart_InvalidJSONDoesNotOverwriteExistingFile(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	existingName := "alpha.json"
	existingContent := `{"type":"codex","email":"alpha@example.com"}`
	if err := os.WriteFile(filepath.Join(authDir, existingName), []byte(existingContent), 0o600); err != nil {
		t.Fatalf("failed to seed existing auth file: %v", err)
	}

	files := []struct {
		name    string
		content string
	}{
		{name: existingName, content: `{"type":"codex"`},
		{name: "beta.json", content: `{"type":"claude","email":"beta@example.com"}`},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, file := range files {
		part, err := writer.CreateFormFile("file", file.name)
		if err != nil {
			t.Fatalf("failed to create multipart file: %v", err)
		}
		if _, err = part.Write([]byte(file.content)); err != nil {
			t.Fatalf("failed to write multipart content: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusMultiStatus, rec.Code, rec.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(authDir, existingName))
	if err != nil {
		t.Fatalf("expected existing auth file to remain readable: %v", err)
	}
	if string(data) != existingContent {
		t.Fatalf("expected existing auth file to remain %q, got %q", existingContent, string(data))
	}

	betaData, err := os.ReadFile(filepath.Join(authDir, "beta.json"))
	if err != nil {
		t.Fatalf("expected valid auth file to be created: %v", err)
	}
	if string(betaData) != files[1].content {
		t.Fatalf("expected beta auth file content %q, got %q", files[1].content, string(betaData))
	}
}

func TestDeleteAuthFile_BatchQuery(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	files := []string{"alpha.json", "beta.json"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(authDir, name), []byte(`{"type":"codex"}`), 0o600); err != nil {
			t.Fatalf("failed to write auth file %s: %v", name, err)
		}
	}

	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(
		http.MethodDelete,
		"/v0/management/auth-files?name="+url.QueryEscape(files[0])+"&name="+url.QueryEscape(files[1]),
		nil,
	)
	ctx.Request = req

	h.DeleteAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected delete status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got, ok := payload["deleted"].(float64); !ok || int(got) != len(files) {
		t.Fatalf("expected deleted=%d, got %#v", len(files), payload["deleted"])
	}

	for _, name := range files {
		if _, err := os.Stat(filepath.Join(authDir, name)); !os.IsNotExist(err) {
			t.Fatalf("expected auth file %s to be removed, stat err: %v", name, err)
		}
	}
}
