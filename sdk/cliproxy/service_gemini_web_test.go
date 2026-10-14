package cliproxy

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps/geminiweb"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestGeminiWebAuthFileRegistersNativeExecutorAndModels(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{AuthDir: dir}
	auths, err := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{Config: cfg, AuthDir: dir, Now: time.Now()}, filepath.Join(dir, "gemini-web.json"), []byte(`{"type":"gemini-web","email":"test@example.test","secure_1psid":"test-cookie","secure_1psidts":"test-ts"}`))
	if err != nil || len(auths) != 1 {
		t.Fatalf("auths=%d err=%v", len(auths), err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	if _, err = manager.Register(context.Background(), auths[0]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auths[0].ID) })
	service.registerExecutorForAuth(auths[0], false)
	registered, ok := manager.Executor("gemini-web")
	if !ok {
		t.Fatal("executor not registered")
	}
	if _, ok = registered.(*executor.GeminiWebExecutor); !ok {
		t.Fatalf("wrong executor %T", registered)
	}
	service.registerModelsForAuth(context.Background(), auths[0])
	models := GlobalModelRegistry().GetAvailableModelsByProvider("gemini-web")
	if len(models) != len(geminiweb.Models()) {
		t.Fatalf("models=%d", len(models))
	}
}
