package management

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/headerdefaults"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const headerVersionRefreshInterval = 3 * time.Hour

type headerVersionSchedule struct{ claude, codex time.Time }

func (s *headerVersionSchedule) due(now time.Time, cfg *config.Config) (bool, bool) {
	due := func(enabled bool, last *time.Time) bool {
		if !enabled {
			*last = time.Time{}
			return false
		}
		if !last.IsZero() && now.Sub(*last) < headerVersionRefreshInterval {
			return false
		}
		*last = now
		return true
	}
	return due(cfg.ClaudeHeaderDefaults.AutoUpdate, &s.claude), due(cfg.CodexHeaderDefaults.AutoUpdate, &s.codex)
}

// RunHeaderDefaultsUpdater stops with the HTTP server and observes newly enabled settings
// within 30 seconds. Failed lookups retain persisted values until the next scheduled check.
func (h *Handler) RunHeaderDefaultsUpdater(ctx context.Context) {
	if h == nil || h.configFilePath == "" {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	var schedule headerVersionSchedule
	for {
		if ctx.Err() != nil {
			return
		}
		h.mu.Lock()
		cfg := h.cfg.CloneForRuntime()
		h.mu.Unlock()
		if cfg != nil {
			claude, codex := schedule.due(time.Now(), cfg)
			if claude || codex {
				client := &http.Client{}
				util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: cfg.ProxyURL}, client)
				versions, failures := headerdefaults.Fetch(ctx, client, claude, codex)
				client.CloseIdleConnections()
				if ctx.Err() != nil {
					return
				}
				for _, failure := range failures {
					log.WithError(failure).Warn("header defaults: keeping existing version after lookup failure")
				}
				if err := h.applyHeaderVersions(ctx, versions); err != nil {
					log.WithError(err).Warn("header defaults: failed to save updated versions")
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// applyHeaderVersions reads again after network activity so concurrent edits and opt-outs
// are preserved. It shares the management writer lock and patches only version fields.
func (h *Handler) applyHeaderVersions(ctx context.Context, versions headerdefaults.Versions) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	raw, err := os.ReadFile(h.configFilePath)
	if err != nil {
		return err
	}
	current, err := config.ParseConfigBytes(raw)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 {
		return fmt.Errorf("invalid header defaults configuration")
	}
	v8 := config.IsV8ConfigLayout(doc.Content[0])
	if v8 {
		normalized, _, errNormalize := config.NormalizeConfigLayout(raw, true)
		if errNormalize != nil {
			return errNormalize
		}
		if err = yaml.Unmarshal(normalized, &doc); err != nil {
			return err
		}
	}
	root := doc.Content[0]
	changed := false
	set := func(path []string, value, previous string) {
		if value == "" || value == previous {
			return
		}
		parent := root
		for _, key := range path[:len(path)-1] {
			next := configV8Node(parent, []string{key})
			if next == nil {
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, next)
			}
			parent = next
		}
		if parent.Kind != yaml.MappingNode {
			return
		}
		key := path[len(path)-1]
		node := configV8Node(parent, []string{key})
		if node == nil {
			node = &yaml.Node{}
			parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, node)
		}
		node.Kind, node.Tag, node.Value = yaml.ScalarNode, "!!str", value
		node.Content = nil
		changed = true
	}
	if hd := current.ClaudeHeaderDefaults; hd.AutoUpdate {
		path := []string{"claude-header-defaults"}
		if v8 {
			path = strings.Split("upstream.claude.header-defaults", ".")
		}
		if versions.Claude != "" {
			set(append(path, "user-agent"), headerdefaults.UserAgentVersion(hd.UserAgent, "claude-cli/", versions.Claude, " (external, cli)"), hd.UserAgent)
		}
		set(append(path, "package-version"), versions.Package, hd.PackageVersion)
		set(append(path, "runtime-version"), versions.Runtime, hd.RuntimeVersion)
	}
	if hd := current.CodexHeaderDefaults; hd.AutoUpdate && versions.Codex != "" {
		path := []string{"codex-header-defaults", "user-agent"}
		if v8 {
			path = strings.Split("oauth.providers.codex.header-defaults.user-agent", ".")
		}
		set(path, headerdefaults.UserAgentVersion(hd.UserAgent, "codex_cli_rs/", versions.Codex, ""), hd.UserAgent)
	}
	if !changed {
		return nil
	}
	data, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	next, err := config.ParseConfigBytes(data)
	if err != nil {
		return err
	}
	if v8 {
		if err = config.ValidateV8Config(data); err != nil {
			return err
		}
	}
	latest, err := os.ReadFile(h.configFilePath)
	if err != nil {
		return err
	}
	if config.ComputeSourceHash(latest) != config.ComputeSourceHash(raw) {
		return fmt.Errorf("configuration changed while preparing header update")
	}
	if err = WriteConfig(h.configFilePath, data); err != nil {
		return err
	}
	next.Home = h.cfg.Home
	h.cfg = next
	h.reloadConfigAfterManagementSaveAsync(ctx, h.reloadSnapshotConfigLocked())
	log.Info("header defaults: saved latest stable software versions and requested config reload")
	return nil
}
