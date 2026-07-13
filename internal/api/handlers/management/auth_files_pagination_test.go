package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFilesSupportsPagination(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("codex-%d.json", i)
		auth := &coreauth.Auth{
			ID:       name,
			FileName: name,
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"runtime_only": "true",
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", name, errRegister)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files?page=2&page_size=2", nil)

	h.ListAuthFiles(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("ListAuthFiles status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
		Page       int `json:"page"`
		PageSize   int `json:"page_size"`
		Total      int `json:"total"`
		TotalPages int `json:"total_pages"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	gotNames := make([]string, 0, len(response.Files))
	for _, file := range response.Files {
		gotNames = append(gotNames, file.Name)
	}
	if want := []string{"codex-2.json", "codex-3.json"}; !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("files = %#v, want %#v", gotNames, want)
	}
	if response.Page != 2 || response.PageSize != 2 || response.Total != 5 || response.TotalPages != 3 {
		t.Fatalf("pagination = page %d size %d total %d pages %d, want 2/2/5/3", response.Page, response.PageSize, response.Total, response.TotalPages)
	}
}

func TestListAuthFilesRejectsInvalidPageSize(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files?page_size=0", nil)

	h.ListAuthFiles(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ListAuthFiles status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestListAuthFilesKeepsLegacyShapeWithoutPagination(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "runtime.json",
		FileName: "runtime.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ctx)

	var response map[string]json.RawMessage
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(response) != 1 {
		t.Fatalf("legacy response fields = %d, want only files; body = %s", len(response), rec.Body.String())
	}
	if _, ok := response["files"]; !ok {
		t.Fatalf("legacy response missing files: %s", rec.Body.String())
	}
}

func TestListAuthFilesFromDiskSupportsPagination(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	for _, name := range []string{"c.json", "a.json", "b.json"} {
		if errWrite := os.WriteFile(filepath.Join(authDir, name), []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
			t.Fatalf("write %s: %v", name, errWrite)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files?page=2&page_size=1", nil)
	h.ListAuthFiles(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("ListAuthFiles status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
		Page  int `json:"page"`
		Total int `json:"total"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(response.Files) != 1 || response.Files[0].Name != "b.json" || response.Page != 2 || response.Total != 3 {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}
}

func BenchmarkListAuthFilesPaginated(b *testing.B) {
	manager := newAuthFilesBenchmarkManager(10_000)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: b.TempDir()}, manager)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files?page=1&page_size=200", nil)
		h.ListAuthFiles(ctx)
		if rec.Code != http.StatusOK {
			b.Fatalf("ListAuthFiles status = %d", rec.Code)
		}
	}
}

func BenchmarkListAuthFilesLegacy(b *testing.B) {
	manager := newAuthFilesBenchmarkManager(10_000)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: b.TempDir()}, manager)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
		h.ListAuthFiles(ctx)
		if rec.Code != http.StatusOK {
			b.Fatalf("ListAuthFiles status = %d", rec.Code)
		}
	}
}

func newAuthFilesBenchmarkManager(count int) *coreauth.Manager {
	manager := coreauth.NewManager(nil, nil, nil)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("codex-%05d.json", i)
		_, _ = manager.Register(context.Background(), &coreauth.Auth{
			ID:       name,
			FileName: name,
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"runtime_only": "true",
			},
		})
	}
	return manager
}
