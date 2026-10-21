# Missing Plugin Selection Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking. Jesse requires a dedicated Sol 6.1 coordinator in its own worktree for this fix.

**Goal:** restore useful plugin access for already-missing saved selections without switching healthy sessions to new revisions.

**Architecture:** produce one effective selected-directory list before sandbox and plugin initialization. Reuse the existing same-store/same-marketplace identity lookup only for missing managed roots. Preserve durable exact-source obligations separately.

**Tech Stack:** Go, existing plugin loader, session configuration, sandbox infrastructure and local registry fixtures.

**Spec:** [Plugin session preservation design](../specs/2026-10-16-plugin-session-preservation-design.md), shared constraints and Fix 2.

## Global Constraints

- Preserve the session's selected revision whenever it exists.
- Preserve selected default-off plugins. Global defaults are for new selection, not revocation of an existing session's selection.
- Preserve exact-source durable skill obligations. Never substitute new bytes for an already admitted continuation.
- Continue using healthy plugins and ordinary conversation when one source fails.
- Run targeted red/green checks locally. Full tests and lint gates belong in CI.
- Tests assert typed state, identities, counts and side effects, never prose.

## Review Focus

1. Existing A with newer B installed: A wins, without hooks or tools silently moving to B. Task 1.
2. Default-off selection, symlinked store and ambiguous same-name installs: preserve identity and reject cross-store substitutions. Task 1.
3. A confined cold delegate with different parent/child revisions: admit the child's effective roots before loading, including an inherited sandbox. Task 2.
4. Exact-source continuation or frozen descendant: keep its source contract, even when fresh invocation can recover. Task 2.
5. Repair fails or another source is malformed: healthy sources and ordinary work stay usable. Task 1 and Task 2.

---

## Ownership and execution

Use coordinator `codex-jesse-at-pr/gpt-6.1-sol` with `isolation="worktree"` after
the preservation fix. Own only the files below and focused tests for their call
sites. Do not implement warning visibility; produce the structured recovery
outcomes the warning plan consumes. No network fetch or live configuration repair.

### Task 1: resolve missing selected roots with typed outcomes

**Files:**
- Create: `internal/plugins/selected_dirs.go`, `internal/plugins/selected_dirs_test.go`.
- Reuse: `internal/plugins/updated_install.go`, `internal/plugins/updated_install_test.go`.

**Interfaces, new in `internal/plugins`:**

```go
type SelectedDirRecovery struct { Previous, Current string }
type SelectedDirFailure struct { Path string; Err error }
type SelectedDirResolution struct {
    Dirs []string
    Recoveries []SelectedDirRecovery
    Failures []SelectedDirFailure
}
func ResolveSelectedDirs(dirs []string) SelectedDirResolution
```

`Dirs` preserves selection order and cardinality: each input remains at its slot,
except a validated missing-root replacement occupies that slot. An unresolved
source remains in `Dirs` for normal fail-soft discovery and actionable reporting.
Unchanged healthy selections produce neither recovery nor failure. Errors belong
to individual sources; they do not abort the complete selection.

- [ ] Add `TestResolveSelectedDirsKeepsExistingRevision`, `TestResolveSelectedDirsRepairsMissingManagedRoot`, and table cases for non-managed explicit paths, explicitly supplied managed-cache paths, duplicate registry candidates, wrong marketplace/store, default-off selection, inaccessible roots, symlink identity and invalid manifests. A valid candidate with a broken hook must still recover its readable skill. Example assertions against fixture paths:

```go
r := ResolveSelectedDirs([]string{healthyOld, missingOld, explicitMissing})
if !slices.Equal(r.Dirs, []string{healthyOld, currentSameIdentity, explicitMissing}) { t.Fatal(r.Dirs) }
if len(r.Recoveries) != 1 || r.Recoveries[0].Previous != missingOld || r.Recoveries[0].Current != currentSameIdentity { t.Fatal(r.Recoveries) }
```

- [ ] Run `go test ./internal/plugins -run '^TestResolveSelectedDirs' -count=1`. Establish red behavior using the existing raw directory list first, then introduce the API; do not treat a compiler error as the only regression proof.
- [ ] Implement `ResolveSelectedDirs`: use `os.Stat`/typed `fs.ErrNotExist` to distinguish missing roots from present-but-invalid or unreadable roots. Only missing managed-cache roots can call `UpdatedInstallDir`, regardless of their original selection provenance; saved `PluginDirs` carries no provenance. Validate candidates with `plugin.ManifestName`, matching skill discovery, not the full component loader. Independent hook/MCP failures remain the component loader's responsibility. Preserve unknown errors as failures rather than retargeting. Do not mutate the input slice or the registry, or add cross-marketplace alias lookup.
- [ ] Run `go test ./internal/plugins -run 'Test(ResolveSelectedDirs|UpdatedInstallDir)' -count=1`. Confirm each fixture's on-disk files and registry stayed unchanged.
- [ ] Stage the two new files and any justified focused changes to `updated_install` files, inspect the staged diff, commit `fix(plugins): resolve missing saved selections by identity`, and run both task reviews.

### Task 2: share effective roots across startup, restore and fresh invocation

**Files:**
- Create: `agent/session_plugin_selection.go`, `agent/session_plugin_selection_test.go`.
- Modify: `agent/session_init.go`, `agent/session_skill_source.go`, `agent/session_config.go`, and session-owned fields in `agent/session.go` if required.
- Inspect/wire all infrastructure callers: `agent/delegate_runtime.go`, `agent/subagents.go`, `agent/sandbox_delegate.go`, `agent/sandbox_infra.go`, `cmd/evener/run.go`, `cmd/evener/serve.go`.
- Preserve contracts in: `agent/session_skill_reload.go`, `agent/schema/skill_lifecycle.go`, and `subagentConfigFromFrozenDescriptor` in `agent/subagents.go`.
- Update: `docs/skills.md`, `docs/product/subsystems.md` S17–S19.

**Interfaces:**
- Consume `plugins.ResolveSelectedDirs(dirs []string) plugins.SelectedDirResolution` from Task 1.
- Add `PrepareSessionPluginSelection(cfg SessionConfig) (SessionConfig, plugins.SelectedDirResolution)` in `agent/session_plugin_selection.go`. It copies config and installs `resolution.Dirs`; it neither creates a session nor runs components.
- Call this before the first sandbox resolution/`SessionInfraRoots` read and before any skill/component loading on each real creation/restore path. Repeated preparation is idempotent. Pass the result forward; do not create independently resolved catalogs and sandbox roots.
- Change the internal method to `prepareSubagentEnvironmentFor(workingDir string, requested *sandbox.SandboxPolicy, childSessionID string, childCfg SessionConfig) (execenv.ExecutionEnvironment, bool, error)`. Infrastructure roots come from prepared `childCfg`, not `s.cfg`. Update every caller, including frozen activation and `delegateRuntime.restoreIdle`, before it provisions the environment. Derive child config through the existing frozen-config conversion; do not rewrite the durable descriptor.
- In cold restore, preparation inside `RestoreSessionFromMetaWithConfig` alone is too late: `delegateRuntime.restoreIdle` has already built the sandbox and passes `sandboxProvisioned: true`. Carry the same prepared selection into the in-memory restore metadata/config and the child's loaders. Preserve `sharedRestoreEnvironment` binding/scratch ownership and the existing sandbox mode/network floor. Inherited confined policy must admit the prepared child roots too.
- Retain `[]plugins.SelectedDirRecovery` in the session until its normal warning publisher can consume it. The warning fix will turn these into full-detail notices; this fix may remain quiet on successful recovery.
- Fresh skill invocation shares the same resolution policy. `invocation.Source != nil` and durable obligations keep their original exact-source path and unavailable result.

- [ ] Add `TestPluginSelectionRecoveryBeforeSandbox`, `TestPluginSelectionRecoveryReachesAllConsumers`, and `TestPluginSelectionRecoveryPreservesExactSource`. Use real restore/session construction and local plugins with a skill, hook/script and companion file. Assert config, discovered skill source, hook execution result, MCP configuration root and descendant selection all use the same repaired path. Assert an existing A stays A after B appears.
- [ ] Add `TestPluginSelectionRecoveryColdDelegateRoots`: parent selects P, frozen child selects missing A, and current same-identity B is available. Restore the actual cold delegate under a restricted sandbox and execute B's companion script. Repeat with healthy child A, which must keep and execute A, and with inherited rather than explicitly requested confinement. Assert parent config, durable descriptor, mode/network policy and environment ownership stay unchanged. Config-only assertions do not prove file access.
- [ ] Before wiring, run `go test ./agent -run '^TestPluginSelectionRecovery' -count=1` and record the stale-root/sandbox failure. Cover a restored session and fresh selection, a default-off saved choice, and two plugins where only one remains unavailable.
- [ ] Wire the preparation through actual creation and restore ordering. Carry effective roots to normal config persistence without rewriting old lifecycle records. For frozen delegates, resolve their own selected roots on activation rather than mutating the durable descriptor in place. Keep current error boundaries and healthy-session continuation. Do not broaden `SessionInfraRoots` to accept arbitrary parents.
- [ ] Rerun the new tests, `go test ./agent -run 'Test.*(PluginSelection|Skill.*(Source|Reload)|SessionInfraRoots)' -count=1`, and targeted CLI sandbox/launch tests in `./cmd/evener` selected from the touched call sites. On Linux, use the repository's real confined-environment harness for the recovered companion script; report an unavailable sandbox harness rather than claiming it ran.
- [ ] Update the owning docs, inspect the staged diff of exact touched files, commit `fix(agent): prepare recovered plugin roots before session initialization`, and complete specification, quality and whole-fix reviews.

## Completion

Report which paths recover, which intentionally remain unavailable, and proof
that existing revisions and exact-source continuations never retarget. Provide the
accepted commit and new recovery types to the warning coordinator. A successful
current-revision recovery must not be described as recovery of deleted old bytes.
