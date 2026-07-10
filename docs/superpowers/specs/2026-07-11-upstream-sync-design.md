# Upstream Sync Design

## Goal

Merge `upstream/main` at `v7.2.65` into CLIProxyAPIPlus without regressing Plus-specific production behavior.

## Context

The previous manual sync merged upstream commit `dd49a520` through merge commit `f00d5ff7`. The current upstream tip is `8c2bf2c2`, adding 187 commits and changing 401 files. A merge-tree preview reports 65 conflicting files: 36 content conflicts and 29 modify/delete conflicts.

The largest architectural change is upstream's removal of the standalone Gemini CLI stack and addition of Google Interactions. Plus still exposes Gemini CLI authentication, execution, translation, and management behavior, so this sync must not silently remove that capability.

## Selected Approach

Perform one full merge of `upstream/main` into an isolated integration branch, then resolve conflicts by behavior domain. Do not cherry-pick the upstream commits individually because the OAuth, plugin, registry, executor, translator, and watcher changes are interdependent.

The merge will preserve Gemini CLI alongside the new Interactions implementation. Compatibility can be reconsidered in a separate change after production usage and feature parity are known.

## Behavior Preservation Contract

The synchronized branch must retain these Plus capabilities:

- standalone Gemini CLI authentication, routing, execution, and translation;
- mixed-provider weighted sticky scheduling and session affinity;
- per-auth OAuth model aliases, provider aliases, and Plus-specific OAuth providers;
- stable Claude and Codex client fingerprints, including persisted Claude device high-water marks;
- Codex quota-exhaustion detection, cooldown state changes, and account failover;
- XAI authentication proxy behavior and websocket handling;
- request logging extensions, successful-stream model naming, and manager proxy routes;
- Docker, plugin persistence, and migrated-server deployment behavior.

The branch must also incorporate these upstream capabilities:

- Google Interactions handlers, translators, thinking provider, and routing;
- OAuth device-flow and cancelable-session refactor;
- persistent cooldown state and unauthorized credential refresh;
- plugin host, plugin store, and home-plugin synchronization improvements;
- current Codex/XAI image, websocket, model-header, usage, and service-tier behavior;
- current model registry metadata and validation rules.

## Conflict Resolution Rules

1. Treat upstream architecture as the default when there is no Plus-specific behavior.
2. Preserve Plus behavior semantically; do not keep obsolete upstream-era code merely to reduce textual conflicts.
3. For Gemini CLI modify/delete conflicts, retain the Plus file and adapt it to current shared interfaces.
4. For scheduler and conductor conflicts, begin from the upstream implementation and reapply Plus weighted-sticky and failover semantics with tests.
5. For executor and translator conflicts, incorporate upstream protocol fixes while retaining Plus request identity, logging, quota, and provider compatibility behavior.
6. Keep `internal/thinking` on its canonical configuration-to-provider translation architecture.
7. Avoid unrelated refactors and formatting changes.

## Execution Boundaries

Work occurs only on `codex/manual-upstream-sync-20260711` in `.worktrees/manual-upstream-sync-20260711`. The existing `master` checkout and its untracked `.omc/` directory remain untouched.

The pre-existing full-suite failure in `TestUseGitHubCopilotResponsesEndpoint_RegistryResponsesOnlyModel` is order-dependent global registry pollution: the test passes repeatedly in isolation. It is recorded as a baseline constraint and will not be bundled into the upstream sync unless the merge makes it deterministic or blocks final verification.

## Verification

Verification proceeds from narrow to broad:

1. Run tests for each resolved package before moving to the next conflict domain.
2. Run targeted Plus sentinel tests for scheduler stickiness, quota failover, fingerprints, XAI proxying, Gemini CLI, Interactions, logging, and plugin persistence.
3. Run `gofmt -w` on changed Go files.
4. Run `go test ./...` and separately rerun the known order-dependent GitHub Copilot test.
5. Run `go build -o test-output ./cmd/server && rm test-output`.
6. Run `git diff --check` and inspect the final merge diff for accidental capability removal.

## 10x Check

The higher-leverage follow-up is a repeatable sync audit that generates the upstream delta, conflict hotspot list, and Plus behavior sentinel matrix automatically. That work is intentionally excluded from this sync so the integration remains focused.

## Platonic Ideal

Users should receive upstream protocol and provider improvements without noticing that a fork synchronization occurred. Existing credentials, routing decisions, model aliases, logs, and deployment workflows should continue to behave identically unless an upstream-compatible improvement is explicitly documented.
