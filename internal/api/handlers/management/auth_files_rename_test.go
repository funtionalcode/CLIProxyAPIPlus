package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestGeminiWebRenameWithVerifiedContent(t *testing.T) {
	for _, scenario := range []string{"success", "exists", "stale", "unsafe", "missing-guard"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("MANAGEMENT_PASSWORD", "")
			gin.SetMode(gin.TestMode)
			dir := t.TempDir()
			manager := coreauth.NewManager(nil, nil, nil)
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, manager)
			old := []byte(`{"type":"gemini-web","email":"old","secure_1psid":"session","secure_1psidts":"timestamp","note":"note","disabled":true}`)
			if err := h.writeAuthFile(context.Background(), "old.json", old); err != nil {
				t.Fatal(err)
			}
			name := "new.json"
			if scenario == "unsafe" {
				name = "../outside.json"
			}
			if scenario == "exists" {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("do-not-overwrite"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			content := `{"type":"gemini-web","email":"new","secure_1psid":"replacement","secure_1psidts":"timestamp","note":"note","disabled":true}`
			body, _ := json.Marshal(map[string]any{"name": "old.json", "new_name": name, "content": content})
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			req := httptest.NewRequest(http.MethodPost, "/v8/management/credentials/rename", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			digest := sha256.Sum256(old)
			if scenario == "stale" {
				digest = sha256.Sum256([]byte("stale"))
			}
			if scenario != "missing-guard" {
				req.Header.Set(authFileWriteContentSHA256Header, hex.EncodeToString(digest[:]))
				req.Header.Set(authFileWriteIdentitiesHeader, url.QueryEscape(`[{"name":"old.json","provider":"gemini-web"}]`))
			}
			ctx.Request = req
			h.RenameAuthFileV8(ctx)
			if scenario != "success" {
				if rec.Code < 400 {
					t.Fatalf("expected rejection: %d", rec.Code)
				}
				data, err := os.ReadFile(filepath.Join(dir, "old.json"))
				if err != nil || !bytes.Equal(data, old) {
					t.Fatal("source was altered on failure")
				}
				if _, ok := manager.GetByID("old.json"); !ok {
					t.Fatal("source runtime removed on failure")
				}
				if scenario == "exists" {
					data, _ = os.ReadFile(filepath.Join(dir, name))
					if string(data) != "do-not-overwrite" {
						t.Fatal("destination overwritten")
					}
				}
				return
			}
			if rec.Code != 200 {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if _, err := os.Stat(filepath.Join(dir, "old.json")); !os.IsNotExist(err) {
				t.Fatal("old file remains")
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil || string(data) != content {
				t.Fatal("saved content mismatch")
			}
			info, _ := os.Stat(filepath.Join(dir, name))
			if info.Mode().Perm() != 0600 {
				t.Fatal("session file permissions must remain private")
			}
			if _, ok := manager.GetByID("old.json"); ok {
				t.Fatal("old runtime remains")
			}
			auth, ok := manager.GetByID(name)
			if !ok || auth.Label != "new" || !auth.Disabled || auth.Metadata["note"] != "note" {
				t.Fatal("new account state not preserved")
			}
		})
	}
}
