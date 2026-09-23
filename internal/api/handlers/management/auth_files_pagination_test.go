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
	if len(response) != 2 || response["observed_at"] == nil {
		t.Fatalf("expected files and cooldown observation timestamp; body = %s", rec.Body.String())
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

func TestListAuthFilesPaginationFromManager(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	registerPaginatedAuthFiles(t, authDir, manager)
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	payload := requestAuthFilesPage(t, handler, "/v0/management/auth-files?page=2&page_size=2")
	if payload.Total != 5 || payload.Page != 2 || payload.PageSize != 2 || !payload.HasMore {
		t.Fatalf("pagination metadata = %#v", payload)
	}
	if got := authFileNames(payload.Files); !equalStrings(got, []string{"Charlie.json", "delta.json"}) {
		t.Fatalf("page names = %#v, want Charlie.json and delta.json", got)
	}

	payload = requestAuthFilesPage(t, handler, "/v0/management/auth-files?page=99&page_size=2")
	if payload.Total != 5 || payload.Page != 3 || payload.PageSize != 2 || payload.HasMore || len(payload.Files) != 1 {
		t.Fatalf("out-of-range page = %#v", payload)
	}
}

func TestListAuthFilesPaginationFromDisk(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	registerPaginatedAuthFiles(t, authDir, nil)
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	payload := requestAuthFilesPage(t, handler, "/v0/management/auth-files?page_size=2")
	if payload.Total != 5 || payload.Page != 1 || payload.PageSize != 2 || !payload.HasMore {
		t.Fatalf("pagination metadata = %#v", payload)
	}
	if got := authFileNames(payload.Files); !equalStrings(got, []string{"Alpha.json", "bravo.json"}) {
		t.Fatalf("page names = %#v, want Alpha.json and bravo.json", got)
	}
}

func TestListAuthFilesPaginationAppliesLookupFiltersBeforePaging(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "shared.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
		t.Fatalf("write shared auth file: %v", errWrite)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"auth-b", "auth-a"} {
		auth := &coreauth.Auth{
			ID:       id,
			Index:    "idx-" + id,
			FileName: fileName,
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path": filePath,
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
	}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	payload := requestAuthFilesPage(t, handler, "/v0/management/auth-files?name=shared.json&page=2&page_size=1")
	if payload.Total != 2 || payload.Page != 2 || payload.PageSize != 1 || payload.HasMore || len(payload.Files) != 1 {
		t.Fatalf("filtered pagination = %#v", payload)
	}
	if got := payload.Files[0]["id"]; got != "auth-b" {
		t.Fatalf("filtered page id = %#v, want auth-b", got)
	}
}

func TestListAuthFilesPaginationDefaultsAndCompatibility(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	registerPaginatedAuthFiles(t, authDir, manager)
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	payload := requestAuthFilesPage(t, handler, "/v0/management/auth-files?page=1")
	if payload.Total != 5 || payload.Page != 1 || payload.PageSize != defaultAuthFilesPageSize || payload.HasMore || len(payload.Files) != 5 {
		t.Fatalf("default pagination = %#v", payload)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	handler.ListAuthFiles(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var unpaginated map[string]json.RawMessage
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &unpaginated); errDecode != nil {
		t.Fatalf("decode unpaginated response: %v", errDecode)
	}
	for _, field := range []string{"total", "page", "page_size", "has_more"} {
		if _, exists := unpaginated[field]; exists {
			t.Fatalf("unpaginated response unexpectedly contains %q: %s", field, recorder.Body.String())
		}
	}
}

func TestListAuthFilesPaginationRejectsInvalidValues(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)

	for _, requestPath := range []string{
		"/v0/management/auth-files?page=0&page_size=10",
		"/v0/management/auth-files?page=invalid&page_size=10",
		"/v0/management/auth-files?page=1&page_size=0",
		"/v0/management/auth-files?page=1&page_size=invalid",
	} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, requestPath, nil)

		handler.ListAuthFiles(ctx)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want %d body=%s", requestPath, recorder.Code, http.StatusBadRequest, recorder.Body.String())
		}
	}
}

func registerPaginatedAuthFiles(t *testing.T, authDir string, manager *coreauth.Manager) {
	t.Helper()
	for index, name := range []string{"delta.json", "Alpha.json", "echo.json", "Charlie.json", "bravo.json"} {
		path := filepath.Join(authDir, name)
		if errWrite := os.WriteFile(path, []byte(`{"type":"codex","email":"user@example.com"}`), 0o600); errWrite != nil {
			t.Fatalf("write auth file %s: %v", name, errWrite)
		}
		if manager == nil {
			continue
		}
		auth := &coreauth.Auth{
			ID:       name,
			Index:    "idx-" + name,
			FileName: name,
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path": path,
			},
			Metadata: map[string]any{"ordinal": index},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", name, errRegister)
		}
	}
}

type authFilesPaginationPayload struct {
	Files    []map[string]any `json:"files"`
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	HasMore  bool             `json:"has_more"`
}

func requestAuthFilesPage(t *testing.T, handler *Handler, requestPath string) authFilesPaginationPayload {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, requestPath, nil)

	handler.ListAuthFiles(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("%s status = %d, want %d body=%s", requestPath, recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload authFilesPaginationPayload
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode %s response: %v", requestPath, errDecode)
	}
	return payload
}

func authFileNames(files []map[string]any) []string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		name, _ := file["name"].(string)
		names = append(names, name)
	}
	return names
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
