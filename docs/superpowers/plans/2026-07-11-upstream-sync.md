# CLIProxyAPIPlus Upstream Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge `upstream/main@8c2bf2c2` (`v7.2.65`) while preserving the Plus behavior contract in the approved sync design.

**Architecture:** Perform one full non-fast-forward merge in the isolated sync worktree. Resolve structural removals first, then reconcile shared auth/scheduler, executor/translator, and API/config/watcher behavior. Verify each domain with targeted tests before committing the merge, then repair only evidence-backed regressions.

**Tech Stack:** Go 1.26+, Git merge, Gin, existing Go test suite

---

### Task 1: Capture the Integration Boundary and Start the Merge

**Files:**
- Reference: `docs/superpowers/specs/2026-07-11-upstream-sync-design.md`
- Merge target: repository-wide changes from `upstream/main`

- [ ] **Step 1: Verify branch, author, and upstream commit**

Run:

```bash
git branch --show-current
git config user.name
git config user.email
git rev-parse upstream/main
git status --short
```

Expected: branch `codex/manual-upstream-sync-20260711`, author `zhengyage <zhengyage@magicpipeline.com>`, upstream `8c2bf2c2...`, and no uncommitted files.

- [ ] **Step 2: Create a backup reference**

Run:

```bash
git branch backup/pre-upstream-sync-20260711-$(date +%H%M%S) HEAD
```

Expected: a backup branch pointing at the design/plan commit before the upstream merge.

- [ ] **Step 3: Start the full merge without committing**

Run:

```bash
git merge --no-ff --no-commit upstream/main
```

Expected: Git stops with the known conflict set while applying all non-conflicting upstream changes.

- [ ] **Step 4: Record the actual conflict inventory**

Run:

```bash
git diff --name-only --diff-filter=U
git ls-files -u
```

Expected: conflicts are limited to the domains identified in the design: Gemini CLI, auth/scheduler, executors/translators, API/config/watcher, and their tests.

### Task 2: Preserve Gemini CLI and Add Interactions

**Files:**
- Preserve: `internal/auth/gemini/`
- Preserve: `internal/runtime/executor/gemini_cli_executor.go`
- Preserve: `internal/runtime/geminicli/`
- Preserve: `internal/thinking/provider/geminicli/`
- Preserve: `internal/translator/**/gemini-cli/`
- Preserve: `sdk/api/handlers/gemini/gemini-cli_handlers.go`
- Preserve: `sdk/auth/gemini.go`
- Integrate: `internal/translator/**/interactions/`
- Integrate: `internal/thinking/provider/interactions/`
- Integrate: `sdk/api/handlers/gemini/interactions_handlers.go`

- [ ] **Step 1: Keep Plus versions of modify/delete Gemini CLI conflicts**

Stage the retained Plus files with:

```bash
git status --porcelain=v1 | awk '$1 == "UD" {print substr($0, 4)}' | while IFS= read -r file; do
  case "$file" in
    internal/auth/gemini/*|internal/runtime/executor/gemini_cli_executor.go|internal/runtime/geminicli/*|internal/thinking/provider/geminicli/*|internal/translator/*/gemini-cli/*|internal/translator/gemini-cli/*|sdk/api/handlers/gemini/gemini-cli_handlers.go|sdk/auth/gemini.go)
      git add -- "$file"
      ;;
  esac
done
```

The index entry must retain the Plus file; do not restore files that were already absent from Plus before the merge.

- [ ] **Step 2: Reconcile registration and shared-interface conflicts**

Resolve these files so both `gemini-cli` and `interactions` formats remain registered:

```text
cmd/server/main.go
internal/api/handlers/management/auth_files.go
internal/api/server.go
internal/cmd/auth_manager.go
internal/config/config.go
internal/constant/constant.go
internal/translator/init.go
internal/watcher/synthesizer/file.go
sdk/cliproxy/service.go
sdk/cliproxy/types.go
sdk/translator/formats.go
test/thinking_conversion_test.go
```

Use the existing registration patterns already present in each file. Keep all Plus provider registrations and append upstream Interactions registrations in the same ordering convention.

- [ ] **Step 3: Format and test both protocol families**

Run:

```bash
gofmt -w cmd/server/main.go internal/api/handlers/management/auth_files.go internal/api/server.go internal/cmd/auth_manager.go internal/config/config.go internal/constant/constant.go internal/translator/init.go internal/watcher/synthesizer/file.go sdk/cliproxy/service.go sdk/cliproxy/types.go sdk/translator/formats.go test/thinking_conversion_test.go
go test ./internal/auth/gemini ./internal/runtime/executor ./internal/runtime/geminicli ./internal/thinking/... ./internal/translator/... ./sdk/api/handlers/gemini ./sdk/auth ./sdk/cliproxy ./test
```

Expected: packages compile with both protocol families; failures are investigated before continuing.

### Task 3: Reconcile Auth, Scheduler, and Persistence Semantics

**Files:**
- Modify: `sdk/cliproxy/auth/conductor.go`
- Modify: `sdk/cliproxy/auth/scheduler.go`
- Preserve: `sdk/cliproxy/auth/selector.go`
- Modify: `sdk/cliproxy/auth/types.go`
- Modify: `sdk/cliproxy/auth/oauth_model_alias.go`
- Modify: `sdk/cliproxy/service.go`
- Modify: `sdk/auth/filestore.go`
- Modify: `sdk/auth/xai.go`
- Modify: `internal/watcher/synthesizer/config.go`

- [ ] **Step 1: Reconcile conductor behavior**

Begin from the upstream conductor flow and retain the Plus quota-exhaustion state transition from commits `0960f4d1` and `a1359796`. Keep upstream persistent cooldown, unauthorized refresh, force mapping, and response-model rewriting.

- [ ] **Step 2: Reconcile scheduling behavior**

Retain upstream scheduler interfaces while preserving `SessionAffinitySelector` and mixed-provider weighted sticky selection from `0bac1cc5`. Keep `selector.go` and its tests because they are active Plus behavior, not obsolete upstream code.

- [ ] **Step 3: Reconcile OAuth aliases and XAI proxy behavior**

Keep upstream device-flow/cancelable-session changes and retain Plus per-auth aliases and proxy propagation from `29207780`. Ensure stored XAI auth records preserve proxy configuration through refresh and reload.

- [ ] **Step 4: Run auth and scheduler regression tests**

Run:

```bash
gofmt -w sdk/cliproxy/auth sdk/cliproxy/service.go sdk/auth internal/watcher/synthesizer
go test ./sdk/cliproxy/auth ./sdk/cliproxy ./sdk/auth ./internal/watcher/synthesizer ./internal/auth/xai
```

Expected: weighted stickiness, quota failover, cooldown persistence, OAuth aliases, and XAI proxy tests pass.

### Task 4: Reconcile Executors, Translators, Usage, and Logging

**Files:**
- Modify: `internal/runtime/executor/claude_executor.go`
- Modify: `internal/runtime/executor/codex_executor.go`
- Modify: `internal/runtime/executor/codex_websockets_executor.go`
- Modify: `internal/runtime/executor/xai_executor.go`
- Modify: `internal/runtime/executor/helps/usage_helpers.go`
- Modify: `internal/translator/codex/claude/`
- Modify: `internal/translator/codex/gemini/`
- Modify: `internal/translator/gemini/openai/responses/`
- Modify: `internal/api/middleware/request_logging.go`
- Modify: `internal/redisqueue/plugin.go`

- [ ] **Step 1: Preserve request identity and failover semantics**

Apply upstream executor protocol changes while retaining stable Claude/Codex identities from `d57854e8` and `6d75786c`, Codex websocket quota classification from `388083b0`, and account failover behavior from `0960f4d1` and `a1359796`.

- [ ] **Step 2: Integrate upstream protocol fixes**

Keep upstream image-tool handling, websocket error mapping, transcript replay, model header overrides, service-tier usage, cache-write tokens, tool-call ordering, and Interactions translations. Preserve Plus-only translator behavior when it is covered by existing tests.

- [ ] **Step 3: Preserve Plus logging behavior**

Retain full upstream request metadata plus Plus stream-success model naming and per-model log filenames from `dafefe60` and `5e3e3cd8`. Ensure no secret or token values are added to logs.

- [ ] **Step 4: Run executor, translator, usage, and logging tests**

Run:

```bash
gofmt -w internal/runtime/executor internal/translator internal/api/middleware internal/redisqueue
go test ./internal/runtime/executor ./internal/runtime/executor/helps ./internal/translator/... ./internal/api/middleware ./internal/redisqueue ./internal/usage
```

Expected: all listed packages pass except a separately documented pre-existing order-dependent failure.

### Task 5: Reconcile API, Configuration, Watchers, and Management Routes

**Files:**
- Modify: `config.example.yaml`
- Modify: `internal/api/handlers/management/oauth_sessions.go`
- Modify: `internal/api/server.go`
- Modify: `internal/registry/model_registry.go`
- Modify: `internal/watcher/synthesizer/config.go`
- Modify: `internal/watcher/synthesizer/config_test.go`
- Modify: `internal/watcher/synthesizer/file.go`
- Modify: `sdk/cliproxy/watcher.go`

- [ ] **Step 1: Merge configuration schemas**

Keep upstream options for Interactions, cooldown persistence, model headers, plugin metadata, and OAuth sessions. Retain Plus options for weighted routing, fingerprints, server behavior, logging, custom providers, and deployment compatibility.

- [ ] **Step 2: Preserve management routes**

Keep upstream OAuth session endpoints and retain the Plus manager proxy route from `02065a68`. Do not reintroduce AMP or Codex conversation paths that the previous sync explicitly removed.

- [ ] **Step 3: Reconcile watcher synthesis**

Ensure hot reload produces both upstream fields and Plus routing/auth fields without resetting persisted state or aliases.

- [ ] **Step 4: Run API, registry, config, and watcher tests**

Run:

```bash
gofmt -w internal/api internal/config internal/registry internal/watcher sdk/cliproxy/watcher.go
go test ./internal/api/... ./internal/config ./internal/registry ./internal/watcher/... ./sdk/cliproxy
```

Expected: routes, config parsing, registry behavior, and hot reload tests pass.

### Task 6: Complete the Merge and Verify the Repository

**Files:**
- Review: all staged merge changes
- Update only if required by evidence: failing implementation or test files

- [ ] **Step 1: Confirm no unresolved entries or conflict markers remain**

Run:

```bash
test -z "$(git diff --name-only --diff-filter=U)"
git grep -n -E '^(<<<<<<<|=======|>>>>>>>)' -- ':!go.sum'
git diff --check
```

Expected: no unresolved paths, conflict markers, or whitespace errors.

- [ ] **Step 2: Run targeted Plus sentinel tests**

Run:

```bash
go test ./sdk/cliproxy/auth ./internal/runtime/executor ./internal/api/... ./internal/watcher/... ./internal/translator/... ./internal/logging ./internal/pluginhost ./internal/pluginstore
go test -count=5 -run '^TestUseGitHubCopilotResponsesEndpoint_RegistryResponsesOnlyModel$' ./internal/runtime/executor
```

Expected: sentinel packages and the isolated baseline test pass.

- [ ] **Step 3: Run full test and required build**

Run:

```bash
go test ./...
go build -o test-output ./cmd/server && rm test-output
```

Expected: build succeeds. Any full-suite failure must be reproduced and classified against the recorded baseline before completion is claimed.

- [ ] **Step 4: Inspect capability-sensitive diffs**

Run:

```bash
git diff --cached --stat
git diff --cached -- internal/runtime/executor sdk/cliproxy/auth internal/translator internal/config internal/api docker-compose.yml Dockerfile
```

Expected: no accidental deletion of the Plus behavior contract.

- [ ] **Step 5: Commit the resolved merge**

Run:

```bash
git commit
```

Use a Chinese Conventional Commit merge subject and a body containing the upstream version, preserved Plus behaviors, test evidence, `Constraint:`, `Scope-risk:`, and `Confidence:`.

### Task 7: Review and Final Handoff

**Files:**
- Review: commits from the pre-sync backup reference through `HEAD`

- [ ] **Step 1: Review final history and worktree state**

Run:

```bash
git log --oneline --decorate -5
git status --short --branch
backup_ref=$(git for-each-ref --sort=-creatordate --format='%(refname:short)' 'refs/heads/backup/pre-upstream-sync-20260711-*' | head -1)
git diff "$backup_ref"..HEAD --stat
```

Expected: the sync branch contains the design, plan, and resolved upstream merge with a clean worktree.

- [ ] **Step 2: Perform final self-review**

Compare the implementation against every bullet in `docs/superpowers/specs/2026-07-11-upstream-sync-design.md`. Report any unmet requirement explicitly rather than treating passing tests as complete coverage.
