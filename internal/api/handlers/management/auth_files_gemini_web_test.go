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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestGeminiWebGuardedEditUpdatesSessionAndDisplayName(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, manager)
	const name = "gemini-web.json"
	original := []byte(`{"type":"gemini-web","email":"old-name","secure_1psid":"old-session","secure_1psidts":"keep-timestamp","note":"keep-note"}`)
	if err := h.writeAuthFile(context.Background(), name, original); err != nil {
		t.Fatal(err)
	}
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: name, Provider: "gemini-web", Model: "gemini-web-3.5-flash-lite", Success: false, Error: &coreauth.Error{Message: "unauthorized", HTTPStatus: 401}})
	before, _ := manager.GetByID(name)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write([]byte(`{"type":"gemini-web","email":"new-name","secure_1psid":"new-session","secure_1psidts":"keep-timestamp","note":"keep-note"}`)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	// The checksum always refers to the current disk version, including persisted status.
	existing, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(existing)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v8/management/credentials", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(authFileWriteContentSHA256Header, hex.EncodeToString(digest[:]))
	req.Header.Set(authFileWriteIdentitiesHeader, url.QueryEscape(`[{"name":"gemini-web.json","runtimeId":"gemini-web.json","provider":"gemini-web","accountSnapshot":"old-name"}]`))
	ctx.Request = req
	h.UploadAuthFile(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	after, ok := manager.GetByID(name)
	if !ok {
		t.Fatal("account missing")
	}
	if after.Label != "new-name" || after.Metadata["secure_1psid"] != "new-session" {
		t.Fatal("runtime name/session not updated")
	}
	if after.CredentialVersion <= before.CredentialVersion {
		t.Fatal("credential version not advanced")
	}
	if state := after.ModelStates["gemini-web-3.5-flash-lite"]; state != nil && state.LastError != nil {
		t.Fatal("unauthorized state not cleared")
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err = json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["email"] != "new-name" || stored["secure_1psid"] != "new-session" || stored["secure_1psidts"] != "keep-timestamp" || stored["note"] != "keep-note" {
		t.Fatal("incorrect saved record")
	}
}
