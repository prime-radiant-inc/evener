# Worktree File Links Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Open filenames in assistant replies in the existing web document pane and native Reader, using the reply's owning session and current worktree.

**Architecture:** Publish installed cwd changes through the existing daemon projection and thread-resync notification, then resolve document roots from fresh owning-session state. Share a pure file-reference contract and a small read-demand scheduler between platform adapters. Keep navigation ownership, read identity and document content separate so worktree changes, pane reuse and recovery cannot display another file's bytes.

**Tech Stack:** Go session/daemon/hub, AppWire, framework-free TypeScript, React/Zustand/dockview, React Native/Expo, existing marked and EnrichedMarkdownText, Vitest, real Git/HTTP/browser fixtures.

**Spec:** `docs/superpowers/specs/2026-10-02-worktree-file-links-design.md`, approved at `a2c5a8af3b2cb9850aec6d38a28827c3bb1fd2e5`. Read the companion `docs/superpowers/specs/2026-10-02-worktree-file-links-review.md` for the two adversarial reviews and resolved traps.

## Global Constraints

The following requirements are copied from the approved spec. Every task includes them.

- Jesse approved assistant messages first: plain paths, inline-code filenames and Markdown links, including remote sessions.
- The feature reads the current file. It does not recover the version that existed when a message was written.
- Recognition runs locally during rendering. It performs no existence checks and fetches no files.
- Both callers import it by package name.
- A missing cwd or missing/invalid session binding leaves the text untouched.
- The source hub/session chooses the document port; transcript text never supplies that authority.
- Keep the shared Markdown renderer's sanitization and URI policy unchanged.
- Preserve both conversations' drafts, scroll and retained identity across placement changes.
- Guaranteed file-link holds, changing assistant `selectable={false}`, and native renderer patches are outside this first feature.
- This feature adds no version fallback or alternate protocol.
- Retry after 1, 2, 4, 8 and then 15 seconds, capped at 15 seconds, with at most one read in flight.
- Keep the 512 KiB cap, binary notices, truncation notice, safe Markdown rendering and image behavior.
- Ordinary transcript-image preview caching remains unchanged.
- No deployment or running-hub restart is part of this request.

Repository constraints: read `AGENTS.md`, `docs/developing-evener/testing.md`, `docs/product/README.md`, `docs/product/subsystems.md`, and `mobile-native/AGENTS.md` before implementation. Keep dependencies and pinned versions unchanged. Use the isolated `wip/worktree-file-links` checkout. Main checkout's `batch-notes.md`, `evener-fluency.exe`, and `notes/experience/isolation-missed-project-config.md` are unrelated and must remain untouched.

## Review Focus

1. A checkpoint samples cwd A while the environment-change carrier commits B: neither a later checkpoint nor reconnect may restore A. Pin this in Tasks 1 and 2.
2. A hydrated filename contains a delegate ID beside an independent delegate ID: file recognition wins for the whole filename and entity controls still work elsewhere. Pin this in Task 6.
3. A retained document is reopened from a different same-session pane, then its source closes or the breakpoint changes: Back follows the latest exact opener without losing parent/delegate work. Pin this in Tasks 5 and 10.
4. The controller stays ready while its remote attachment fails, or an unrelated hub reconnects: only the visible viewer's owning source retries, single-flight, and old identity bytes never return. Pin this in Tasks 4, 7 and 8.
5. A pathname contains Markdown entities, encoded punctuation or an image updated within the HTTP cache lifetime: parsing agrees across web/native and fresh viewer generations obtain new bytes. Pin this in Tasks 3, 9 and 10.

---

## Approval boundary and starting state

This is a plan, not an implementation report. At writing time, the checkout is clean at `35046961750039400cd38e23858858d9fcc58706`. Jesse approved the written spec; implementation-plan review and execution-method selection are still pending. Do not install dependencies, change product code or tests, or run feature tests before that approval. All commands below are future execution instructions.

The following are verified existing seams, not proposed APIs:

- `Session.swapEnvAndRefresh(next *execenv.LocalExecutionEnvironment, record func()) error` installs `env` and `envInfo` under `Session.mu` in `agent/session_env_swap.go`.
- `events.EnvironmentData` describes persisted prompt environment text. It is not a cwd-change carrier.
- `ThreadEnvelopeSource.SessionMeta() schema.SessionMeta` is the existing checkpoint source. `SessionMeta.EnvInfo.WorkingDir` contains the installed cwd.
- `NotifyEvenerThreadResync` and `appwire.ThreadResyncParams{ThreadID, Ref, BootGeneration, Epoch}` already exist. Add no AppWire notification or invented `Reason` field.
- `sourceForThreadWithDeletionFence(ctx, cfg, sources, ref, threadID)` routes an owning source. `Source.ReadThread(ctx, appwire.ThreadReadParams{Ref, ThreadID, IncludeTurns: false})` obtains its current projection.
- `schema.LoadSessionMeta(stateDir, sessionID)` reads persisted metadata. `Past.Find` and roster launch `WorkingDir` values are not current document-root authority.
- `DocPort`, `readDocFile`, `docFileRawURL`, `docImageURL`, `isImagePath`, and typed `host-unsupported` already live in `appwire-client/typescript/docContent.ts`.
- `workspaceStore`, `OpenPaneRecord`, `openPane`, retained transcript origins, `DockHost` and mobile `StackHost` already own pane state.
- Native `ConnectionProvider` has one `activeProfile`, `client` and connection `state`, plus configured `profiles`. It has no per-hub attachment subscription.

### Files and responsibilities

| Unit | Owning files | Responsibility |
| --- | --- | --- |
| Installed-cwd publication | `agent/session_env_swap.go`, `agent/events/{events,payloads,eventdata}.go`, `server/{bridge,thread_envelope,appwire_runtime}.go` | Carry installed cwd, commit it before resync, prevent stale checkpoint rollback |
| Document authority | `cmd/evener-hub/{doc_serve,app_rpc,image_serve}.go`, `cmd/evener-hub/internal/fspaths/paths.go` | Fresh live/persisted roots, current trusted-root alias containment |
| Shared references | New `appwire-client/typescript/fileReferences.ts`, existing `docContent.ts` | Parse literal/URI candidates, retain display/cwd/absolute target/provenance |
| Shared read demand | New `appwire-client/typescript/documentReadDemand.ts`, existing `docContent.ts` | Scoped single-flight generations, bounded retry, viewer image URL |
| Web opening ownership | `frontend/src/shell/{workspace,DockHost}.tsx` or `.ts`, `shell/mobile/StackHost.tsx`, `panes/doc/openDoc.ts` | Retained source promotion, separate document binding/origin, visibility |
| Web recognition | `frontend/src/panes/session/transcript/messages/AgentMarkdown.tsx`, new adjacent `fileReferenceDOM.ts`, `transcript/EntityText.tsx`, `transcriptDisplay/renderContext.tsx` | Reversible file-first/entity-second assistant enhancement |
| Web reads | `frontend/src/panes/doc/DocPane.tsx`, new adjacent `useDocumentRead.ts` | Owning-session hydration, identity replacement, visible recovery, image state |
| Native reads | `mobile-native/src/reader/{documentSource,useDocument,ReaderScreen}.ts` or `.tsx`, `src/screens.tsx` | Bound Reader routes, remote reads, scoped recovery, Reload |
| Native recognition | New `mobile-native/src/reader/markdownFileReferences.ts`, `src/{MarkdownResponse,TimelineItem,screens}.tsx`, `reader/documentReferences.ts` | Source-span transform, validated event identifiers, shared chip destinations |
| Behavior qualification | New hub worktree/browser tests, frontend guard fixtures, native pinned-parser probe | Actual producer/routing/cache/layout and honest native qualification |
| Evergreen behavior | New `docs/product/documents.md`, affected guides/map | Current-root semantics and recovery/navigation ownership |

Here and below, `frontend/` abbreviates `cmd/evener-hub/frontend/` only in prose. Commands and file lists use full paths. New names in the Interfaces sections are proposed APIs to implement, not claims that they exist today.

### Execution and checks

Execute Tasks 1–10 in order. Task 3 is independently reviewable but its contract must settle before platform changes. Keep one writer in this checkout. If using subagents, give each task its listed files and interfaces, make it finish its red/green cycle, and review before handing the next task the updated branch.

After approval, use `make web-preflight` for the frontend install. Inspect `scripts/native/native-preflight.sh` and `scripts/sdk/api-package-preflight.sh` before the corresponding gate. Never run `npm ci` through a symlinked `node_modules`. Native needs a real local install; if missing, run `npm ci --prefix mobile-native` only after confirming that path is not a symlink. Do not change lockfiles to make a check pass.

Each task records its failing command, the failure that proves the missing behavior, the passing command and exact files committed. A compile failure for a proposed API is the first red; also run the behavior test against the first compiling version before accepting green. Format touched TypeScript from its owning frontend/native directory, not the repository root. Read staged diffs before normal commits; never bypass hooks.

## Task 1: Publish installed cwd and fence stale checkpoints

**Files:**
- Modify: `agent/session_env_swap.go`.
- Modify: `agent/events/events.go`, `agent/events/payloads.go`, `agent/events/eventdata.go` and their existing payload/discriminator tests.
- Modify: `server/bridge.go`, `server/thread_envelope.go`, `server/appwire_runtime.go`.
- Test: `agent/session_tools_worktree_create_test.go`, `agent/session_tools_worktree_switch_test.go`, `server/thread_envelope_test.go`, `server/thread_envelope_test_helpers_test.go`.

**Interfaces:**
- Consumes: existing `Session.swapEnvAndRefresh`, lossless event consumption, `ThreadEnvelopeSource.SessionMeta`, `BridgeEvent` and `NotifyEvenerThreadResync`.
- Produces: new `events.EnvironmentChangedData{WorkingDir string}` with JSON `working_dir`, new `EventEnvironmentChanged`, and its required `eventKind()` discriminator.
- Produces: current installed cwd in existing `Thread.CWD`, including seeded/restored/checkpoint projections. No new live-session getter and no read-time sampling inside the envelope delivery cut.

- [ ] **Step 1: Add red producer and projection tests.** Use the existing real-Git `newWorktreeRepo(t)` fixture. Capture the carrier through `ConsumeEventsLossless`; the channel is a synchronization boundary, not a sleep.

```go
func TestWorktreeCreate_PublishesInstalledCWD(t *testing.T) {
    t.Parallel()
    r := newWorktreeRepo(t)
    ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
    defer cancel()
    changed := make(chan string, 8)
    r.s.ConsumeEventsLossless(func(ev events.SessionEvent) {
        if data, ok := ev.Data.(events.EnvironmentChangedData); ok {
            changed <- data.WorkingDir
        }
    }, func() {})
    result, err := r.create(t, map[string]any{"name": "document-lane"})
    if err != nil { t.Fatal(err) }
    want := result["path"].(string)
    select {
    case got := <-changed:
        if got != want { t.Fatalf("published cwd = %q, want %q", got, want) }
        if installed := r.s.currentEnv().WorkingDirectory(); installed != got {
            t.Fatalf("published %q before installation %q", got, installed)
        }
    case <-ctx.Done():
        t.Fatal("installed cwd was not published")
    }
}
```

Import `primeradiant.com/evener/agent/events`; `context` and `time` already belong to this test file. The timeout is a failure guard, not synchronization: success requires receiving the actual carrier. Extend the create/switch/exit family with the same carrier assertion and a failed-switch assertion that publishes no replacement. In `server/thread_envelope_test.go`, use `stubThreadEnvelopeSource.parkOnMeta`/`parkAfterMeta` to hold a checkpoint sample of A, commit an environment carrier for B, release A, and assert both the resync-time read and the subsequent read still report B. Check start/restoration seeding and a stale `SetWorkingDir` seed as well.

- [ ] **Step 2: Run the red tests.**

```bash
go test ./agent -run '^TestWorktree(Create|Switch|Exit)_.*(PublishesInstalledCWD|FailedSwitchKeepsCWD)$' -count=1
go test ./server -run '^Test.*(CWD|EnvelopeCommittedBeforeTheCut|EnvelopeLeadsTheCommit|ThreadReadUnderTheCut)' -count=1
```

Expected: missing carrier initially, then a behavior failure showing absent resync/cwd update or stale A rollback. Keep current envelope-order and no-session-under-cut tests strict.

- [ ] **Step 3: Implement the installed-state carrier and ordered projection.** Capture the installed value while holding `Session.mu`; emit only after releasing it. Use the existing event constructor/emitter pattern in this file's package.

```go
// Proposed payload and discriminator, not a prompt/transcript environment update.
const EventEnvironmentChanged EventKind = "ENVIRONMENT_CHANGED"
type EnvironmentChangedData struct {
    WorkingDir string `json:"working_dir"`
}
func (EnvironmentChangedData) eventKind() EventKind { return EventEnvironmentChanged }
```

At `swapEnvAndRefresh`, the value carried must be the installed `ei.WorkingDir`, not requested arguments or the old environment. In the bridge's common-root commit, update cwd and its publication generation under the projection lock, then enqueue the existing thread-resync notification in that same commit. Announce only after the committed projection is readable.

Checkpoint refresh captures the cwd publication generation before sampling `SessionMeta()` outside `Server.mu`. Apply the sample's `EnvInfo.WorkingDir` only if that generation is unchanged. A carrier increments the generation. Seed from `SessionMeta` when wiring the envelope source; keep launch status as initialization, never as an override of a committed installed cwd. Use this same generation-owned field for checkpoint and carrier updates so a sampled A cannot overwrite committed B. Add the payload to sealed-union/serialization coverage. Do not reuse `EnvironmentData`, alter message text, or add a protocol notification.

- [ ] **Step 4: Run green and impacted lock guards.**

```bash
go test ./agent/events ./server -count=1
go test ./agent -run 'Worktree|EnvelopeSamplingNeverBlocksOnAnEmitHeldLock|EverySamplingRelevantMutexIsClassified|EmitReacquiresSessionMu' -count=1
```

Expected: all selected tests finish with zero failures. Confirm the new event never calls back into a held session mutex.

- [ ] **Step 5: Review and commit this unit.** Run `gofmt` on the changed Go files, stage only the files listed above that changed, inspect `git diff --cached`, then commit `fix: publish installed session working directories`.

## Task 2: Read from fresh owning roots and prove actual worktree routing

**Files:**
- Modify: `cmd/evener-hub/doc_serve.go`, `cmd/evener-hub/app_rpc.go`, `cmd/evener-hub/image_serve.go` and direct root-helper callers found with `rg 'sessionCWD|localSessionCWD|sessionDocumentFromHub' cmd/evener-hub`.
- Modify: `cmd/evener-hub/internal/fspaths/paths.go`.
- Test: `cmd/evener-hub/doc_serve_test.go`, `cmd/evener-hub/doc_serve_confinement_test.go`, `cmd/evener-hub/session_document_route_test.go`, `cmd/evener-hub/app_rpc_session_document_test.go`.
- Create: `cmd/evener-hub/doc_worktree_integration_test.go`.
- Documentation ownership: `docs/product/documents.md` is created in Task 10 after client behavior is checked; retain the root/publication notes for it.

**Interfaces:**
- Consumes: Task 1's fresh live `Thread.CWD`; existing source registry and deletion fence; `schema.LoadSessionMeta(stateDir, sessionID)`; `fspaths.ResolveInRoot(root, path) (string, error)`.
- Produces: context-aware `sessionCWD(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, session string) (string, error)` and updated `sessionDocumentFromHub`/local HTTP/image callers passing their real request context and registry.
- Produces: unchanged HTTP/AppWire file/image protocols with fresh authority. Live-source failure is unavailable, never permission to read a cached launch root. Preserve 403/404/501 and transient 503 distinctions.

- [ ] **Step 1: Add a small archived-cache red test.** Use `docServeTestServer`, warm `web.cfg.Past.Find(sessionID)`, save replacement metadata with cwd B using `schema.SaveSessionMeta`, leave the index warm and request the conflicting file.

```go
func TestDocFile_ArchivedRootBypassesWarmPastMetadata(t *testing.T) {
    web, rootA, sessionID := docServeTestServer(t)
    rootB := t.TempDir()
    for root, contents := range map[string]string{rootA: "launch A", rootB: "current B"} {
        if err := os.WriteFile(filepath.Join(root, "plan.md"), []byte(contents), 0o600); err != nil {
            t.Fatal(err)
        }
    }
    entry := web.cfg.Past.Find(sessionID)
    meta, err := schema.LoadSessionMeta(entry.StateDir, entry.ID)
    if err != nil { t.Fatal(err) }
    meta.EnvInfo.WorkingDir = rootB
    if err := schema.SaveSessionMeta(entry.StateDir, meta); err != nil { t.Fatal(err) }
    if cached := web.cfg.Past.Find(sessionID).Meta.EnvInfo.WorkingDir; cached != rootA {
        t.Fatalf("fixture did not preserve stale A metadata: %q", cached)
    }
    response := docRawRequest(t, web, sessionID, "plan.md")
    if response.Code != http.StatusOK || response.Body.String() != "current B" {
        t.Fatalf("document = %d %q, want current B", response.Code, response.Body.String())
    }
}
```

Retain the fixture's canonical ref spelling. For paths containing `?`, `#`, `%` or spaces, pass `url.QueryEscape(path)` to `docRawRequest`; that helper does not escape its argument.

- [ ] **Step 2: Add the real session/daemon/hub fixture and live red journey.** Define `newWorktreeDocumentFixture(t *testing.T) *worktreeDocumentFixture` in the new test file. Its bounded API is:

```go
type worktreeDocumentFixture struct {
    session *agent.Session
    hubClient *appwire.Client
    hubURL string
    documentURL string
    ref string
    sessionID string
    root string
    step func(t *testing.T, operation, name string) string
    localRead func(t *testing.T, path string) (int, []byte)
    remoteRead func(t *testing.T, path string) (int, []byte)
}
```

Construct it from the actual wiring in `cmd/evener-hub/app_session_activity_relay_test.go:153-223`: real temporary Git repository and commit, `llm.NewClient`, a channel-scripted provider adapter registered at the LLM boundary, `agent.NewSession`, `daemonserver.NewServer`, prepared/replaced AppWire identity, `WireTranscriptHistory`, `ConsumeEventsLossless` into `BridgeEvent`, and the daemon's actual WebSocket server. Register `NewLocalDaemonSourceWithEntries` in `appsource.NewRegistry`, create a real warm PastIndex and Roster, and serve `newHubAppServer` plus `NewWebServer` with that registry. Initialize a real `appwire.Client` using `dialHubRPC`. Connect a controller through existing `controllerOverHost` so `remoteRead` uses the actual remote document proxy.

The provider adapter's `Complete` returns `activityRelayTool("manage_worktree", args)` followed by a terminal assistant response. Drive each step with public `session.ProcessInput(ctx, input, nil)`. The `step` closure queues the tool arguments `{operation, name}` where name applies and returns the installed cwd after the successful run. Git init/config/add/commit use the fixture's temporary directory and explicit test author, never ambient credentials. Reads issue escaped HTTP requests to the fixture servers. Synchronize on actual notifications/client reads through channels; no polling sleeps, mocked directory swap, or mocked document endpoint.

The journey must assert these independent facts, not derive expected bytes with the production root resolver:

```go
// In TestDocumentWorktree_ProducerToLocalAndRemoteReads:
rootB := fixture.step(t, "create", "docs-b")
if err := os.WriteFile(filepath.Join(fixture.root, "plan.md"), []byte("launch A"), 0o600); err != nil { t.Fatal(err) }
if err := os.WriteFile(filepath.Join(rootB, "plan.md"), []byte("worktree B"), 0o600); err != nil { t.Fatal(err) }
for _, read := range []func(*testing.T, string) (int, []byte){fixture.localRead, fixture.remoteRead} {
    status, body := read(t, "plan.md")
    if status != http.StatusOK || string(body) != "worktree B" { t.Fatalf("read = %d %q", status, body) }
    status, body = read(t, filepath.Join(fixture.root, "plan.md"))
    if status != http.StatusForbidden { t.Fatalf("old absolute target returned %d %q", status, body) }
}
```

Declare `fixture := newWorktreeDocumentFixture(t)` at test start. Also write a B-only file. Create C, switch to B and exit to A. After each operation assert installed session cwd, daemon thread cwd, subscribed hub client's hydrated cwd, and local/remote response bytes. Hold hydration during A→B to prove captured A absolute reads fail rather than returning B. Reconnect the real client and read absolute B after resync. Include a live-source-unavailable test with stale past/roster A present: response must be 503, with no A bytes.

- [ ] **Step 3: Run the red backend journey.**

```bash
go test ./cmd/evener-hub -run 'TestDocFile_ArchivedRootBypassesWarmPastMetadata|TestDocumentWorktree_' -count=1
```

Expected: cached A bytes, wrong hydrated cwd, or failure to serve B. Fix test-fixture compilation separately from the behavior failure and record both.

- [ ] **Step 4: Implement fresh authority and narrow trusted-root normalization.** Determine live ownership through the roster, then use `sourceForThreadWithDeletionFence` and the owning `Source.ReadThread` to read current cwd. For an archived local session, use trusted PastIndex `ID`/`StateDir` only as locators and load current metadata from disk. Check deletion ownership through the existing fence. Do not reverse two caches and call it fresh. Do not use cached `entry.Meta` as authority, fall back from failed live reads, or accept a state path from the request.

Pass context/registry through file HTTP, image HTTP and owning AppWire document/image handlers. Keep project grouping and restore-directory metadata unchanged. Normalize only a request's exact current trusted cwd prefix to the resolved root, on a separator boundary:

```go
// Inside ResolveInRoot, after resolving trusted root but before target containment.
if filepath.IsAbs(requested) && trusted != resolved {
    relative, err := filepath.Rel(trusted, requested)
    if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
        requested = filepath.Join(resolved, relative)
    }
}
```

Here `trusted` is the cleaned current cwd, `resolved` is its symlink-resolved root, and `requested` is the existing path parameter. Retain existing lexical containment, target `EvalSymlinks`, non-regular-file checks and final `os.Root` open. This mapping never authorizes an old A root, arbitrary alias or alias sibling.

- [ ] **Step 5: Add containment cases and run green.** A real `alias -> real` fixture must serve identical bytes through relative, current-alias absolute and canonical absolute paths. Alias sibling prefixes, old-worktree roots, escaping child symlinks and a child-link replacement before final open must fail. Keep traversal, FIFO/non-regular, cap and remote-route suites.

```bash
go test ./cmd/evener-hub/internal/fspaths -count=1
go test ./cmd/evener-hub -run 'Doc|Document|SessionImage|DocumentWorktree' -count=1
```

Expected: all selected behavior tests pass. Run the real producer journey again after any root-helper change.

- [ ] **Step 6: Review and commit.** Format changed Go files, stage exactly the changed files in this unit, inspect the staged diff, and commit `fix: resolve documents from current owning session roots`.

## Task 3: Define and prove the shared file-reference contract

**Files:**
- Create: `appwire-client/typescript/fileReferences.ts`, `appwire-client/typescript/fileReferences.test.ts`.
- Modify: `appwire-client/typescript/docContent.ts`, `appwire-client/typescript/README.md`.
- Test: existing `appwire-client/typescript/docContent.test.ts`.

**Interfaces:**
- Export new APIs through the existing `@evener/appwire-client/docContent` subpath. Add no package dependency, resolver alias or new public subpath.

```ts
export type FileReferenceKind = "prose" | "code" | "link";
export interface FileReference {
  readonly path: string;
  readonly cwd: string;
  readonly readTarget: string;
  readonly provenance: "relative" | "absolute";
}
export interface FileReferenceSpan {
  readonly start: number;
  readonly end: number;
  readonly reference: FileReference;
}
export function bindFilePath(path: string, cwd: string): FileReference | undefined;
export function parseFileReference(value: string, kind: FileReferenceKind, cwd: string): FileReference | undefined;
export function findFileReferences(text: string, cwd: string): FileReferenceSpan[];
export function rebindFileReference(reference: FileReference, cwd: string): FileReference;
```

`bindFilePath` binds raw structured file paths, including spaces, without URI interpretation or prose punctuation stripping. `parseFileReference` applies the surface grammar before binding. `findFileReferences` returns offsets in the original eligible prose, excluding stripped punctuation/location suffixes. `rebindFileReference` is an explicit viewer transition: relative targets follow the new cwd; absolute targets stay absolute even if now outside the root. In that latter state the server returns forbidden; recognition never creates a new out-of-root action.

- [ ] **Step 1: Add a table-driven red parser suite.** Include the spec's entire matrix, with independently written expected values. Core assertions:

```ts
import { expect, it } from "vitest";
import { findFileReferences, parseFileReference, rebindFileReference } from "./fileReferences";

it("keeps display identity separate from captured read authority", () => {
  const relative = parseFileReference("./docs//plan.md:12:4", "code", "/work/a");
  expect(relative).toEqual({ path: "docs/plan.md", cwd: "/work/a", readTarget: "/work/a/docs/plan.md", provenance: "relative" });
  const absolute = parseFileReference("/work/a/docs/plan.md", "code", "/work/a");
  expect(absolute).toEqual({ path: "docs/plan.md", cwd: "/work/a", readTarget: "/work/a/docs/plan.md", provenance: "absolute" });
  if (!relative || !absolute) throw new Error("expected accepted references");
  expect(rebindFileReference(relative, "/work/b").readTarget).toBe("/work/b/docs/plan.md");
  expect(rebindFileReference(absolute, "/work/b").readTarget).toBe("/work/a/docs/plan.md");
});

it("decodes link pathnames once, after removing raw metadata", () => {
  expect(parseFileReference("./docs/a%3A12%23L4%3F.md#L9", "link", "/work/a")?.path).toBe("docs/a:12#L4?.md");
  expect(parseFileReference("./docs/100%2525.md", "link", "/work/a")?.path).toBe("docs/100%25.md");
  expect(parseFileReference("docs/100%25.md", "code", "/work/a")?.path).toBe("docs/100%25.md");
  expect(parseFileReference("README.md:12", "link", "/work/a")).toBeUndefined();
  expect(parseFileReference("./README.md%3A12", "link", "/work/a")?.path).toBe("README.md:12");
});

it("never recognizes a shorter suffix of an invalid token", () => {
  for (const text of ["../docs/plan.md", "https://host/docs/plan.md", "mailto:a/docs/plan.md", "docs/foo(bar)/plan.md"]) {
    expect(findFileReferences(text, "/work/a")).toEqual([]);
  }
  expect(findFileReferences("“docs/設計.md:12.” docs/a.md…", "/work/a").map((span) => span.reference.path)).toEqual(["docs/設計.md", "docs/a.md"]);
});
```

Add Jesse's exact two examples; multiple references; code `README.md`, `src/Makefile`, commands and whitespace; explicit extensionless targets and encoded spaces; malformed percent escapes; encoded `..`, slash, controls and leading `//`; literal percent/hash/question-mark data; repeated/interior separators; missing cwd; sibling prefixes; scheme/email/domain/version tokens; directory forms and terminal punctuation. Ref validation belongs to adapters, not this path parser.

- [ ] **Step 2: Run red.**

```bash
cd cmd/evener-hub/frontend
npm test -- ../../../appwire-client/typescript/fileReferences.test.ts
```

Expected: missing exports, then behavior failures against the first compiling parser. Frontend `vite.config.ts` includes shared-package tests; do not add another test runner.

- [ ] **Step 3: Implement lexical parsing with explicit operation order.** Use separate literal and URI paths, then one lexical binder. Reject `..` before collapsing `.` or repeated interior separators. For links, reject raw schemes/protocol-relative/query-only/fragment-only values, remove raw metadata and raw numeric location suffix, decode exactly once in `try/catch`, then validate. For literals, strip only supported location suffixes and never percent-decode. Prose candidates are whole delimited tokens, not suffix regex matches.

```ts
export function rebindFileReference(reference: FileReference, cwd: string): FileReference {
  return {
    ...reference,
    cwd,
    readTarget: reference.provenance === "relative"
      ? `${cwd.replace(/\/+$/, "")}/${reference.path}`
      : reference.readTarget,
  };
}
```

Require a nonempty trusted cwd at callers before rebinding. Handle cwd `/` without generating a double slash. Keep Unicode data, reject controls/backslashes, and derive relative identity from absolute paths only on a cwd separator boundary. Do not resolve symlinks or check existence. Keep `cwdRelative` callers outside this feature working; use the new binder for opening actions rather than silently broadening unrelated behavior.

- [ ] **Step 4: Run green and document exports.**

```bash
cd cmd/evener-hub/frontend
npx biome check --write ../../../appwire-client/typescript/fileReferences.ts ../../../appwire-client/typescript/fileReferences.test.ts ../../../appwire-client/typescript/docContent.ts
npm test -- ../../../appwire-client/typescript/fileReferences.test.ts ../../../appwire-client/typescript/docContent.test.ts
npm run typecheck
```

Update the package README's existing document-helper section with the three distinct values, literal/URI contract and explicit rebind semantics.

- [ ] **Step 5: Review and commit.** Stage the new module/test, changed `docContent.ts` and package README, inspect the diff, and commit `feat: share source-bound file reference parsing`.

## Task 4: Share bounded read demand and viewer image generations

**Files:**
- Create: `appwire-client/typescript/documentReadDemand.ts`, `appwire-client/typescript/documentReadDemand.test.ts`.
- Modify: `appwire-client/typescript/docContent.ts`, `appwire-client/typescript/docContent.test.ts`, `appwire-client/typescript/README.md`.

**Interfaces:**
- Export through `@evener/appwire-client/docContent`; no second document cache.

```ts
export type DocumentReadOutcome = "success" | "transient" | "terminal";
export interface DocumentReadAttempt {
  readonly generation: string;
  isCurrent(): boolean;
}
export interface DocumentReadDemand {
  setActive(active: boolean): void;
  refresh(): void;
  replace(): void;
  dispose(): void;
}
export function createDocumentReadDemand(
  read: (attempt: DocumentReadAttempt) => Promise<DocumentReadOutcome>,
): DocumentReadDemand;
export function docImageReadURL(origin: string, session: string, readTarget: string, generation: string): string;
```

`setActive` combines platform visibility and foreground. `refresh` is explicit open/reopen/Reload or owning controller recovery. `replace` immediately retires old publication and queues a new identity's read; the platform clears content synchronously before calling it. Inactive/disposed attempts cannot publish. At most one physical read runs even across identity replacement; a pending replacement waits for the old operation to settle, whose output is ignored.

- [ ] **Step 1: Add controlled-clock red tests of the real scheduler.** A test-supplied read function is the transport boundary, not a fake scheduler. Use Vitest fake timers, deferred promises and call logs to assert the actual state machine.

```ts
it("recovers an attachment while the controller stays ready", async () => {
  vi.useFakeTimers();
  const seen: string[] = [];
  let healthy = false;
  const demand = createDocumentReadDemand(async (attempt) => {
    seen.push(attempt.generation);
    return healthy ? "success" : "transient";
  });
  demand.setActive(true);
  await vi.advanceTimersByTimeAsync(0);
  expect(seen).toHaveLength(1);
  for (const delay of [1000, 2000, 4000, 8000, 15000, 15000]) {
    await vi.advanceTimersByTimeAsync(delay - 1);
    const count = seen.length;
    await vi.advanceTimersByTimeAsync(1);
    expect(seen).toHaveLength(count + 1);
  }
  healthy = true;
  await vi.advanceTimersByTimeAsync(15000);
  const settled = seen.length;
  await vi.advanceTimersByTimeAsync(60000);
  expect(seen).toHaveLength(settled);
  expect(new Set(seen).size).toBe(seen.length);
  demand.dispose();
  expect(vi.getTimerCount()).toBe(0);
  vi.useRealTimers();
});
```

Use `try/finally` or suite cleanup so fake timers restore even after assertion failure. Add terminal 403/404/501 outcome, hide/background, resume, close, replacement during a deferred read, repeated refresh coalescing, no overlap, rejected-read transient handling, and stale `isCurrent()` tests. Confirm retries increment generations without resetting backoff. Image URL tests compare two generations and verify ordinary `docImageURL` is byte-unchanged.

- [ ] **Step 2: Run red.**

```bash
cd cmd/evener-hub/frontend
npm test -- ../../../appwire-client/typescript/documentReadDemand.test.ts ../../../appwire-client/typescript/docContent.test.ts
```

- [ ] **Step 3: Implement the small state machine.** Own only active/disposed flags, one in-flight promise, one pending demand, one timeout, retry index and a publication epoch. Generate a viewer-unique URL token from a process seed plus a monotonically increasing attempt counter; it is a cache discriminator, not a credential. Retiring an epoch makes all earlier `isCurrent()` checks false. Start pending demand after the current read settles; never start a second operation to work around a slow first one.

```ts
const retryMilliseconds = [1000, 2000, 4000, 8000, 15000] as const;
// On a current transient completion, schedule exactly one timeout:
const delay = retryMilliseconds[Math.min(retryIndex, retryMilliseconds.length - 1)];
retryIndex += 1;
// On success/terminal/inactive/disposed/replacement, clear that timeout.
```

These are implementation fragments inside `createDocumentReadDemand`, whose closure owns `retryIndex`. Explicit refresh/replacement starts a new demand series; retry timeout advances the same series. Read exceptions become transient outcomes. Check current epoch again after awaiting, before scheduling or publishing. Hiding retires publication and timers; reactivation requests a useful read. Do not store file bytes in this module.

```ts
export function docImageReadURL(origin: string, session: string, readTarget: string, generation: string): string {
  return `${docImageURL(origin, session, readTarget)}&read=${encodeURIComponent(generation)}`;
}
```

Keep normal preview URL construction unchanged. Use built-in timers and no platform/network imports.

- [ ] **Step 4: Run green, typecheck and update the package README.**

```bash
cd cmd/evener-hub/frontend
npx biome check --write ../../../appwire-client/typescript/documentReadDemand.ts ../../../appwire-client/typescript/documentReadDemand.test.ts ../../../appwire-client/typescript/docContent.ts ../../../appwire-client/typescript/docContent.test.ts
npm test -- ../../../appwire-client/typescript/documentReadDemand.test.ts ../../../appwire-client/typescript/docContent.test.ts
npm run typecheck
```

- [ ] **Step 5: Review and commit.** Stage this unit's exact changed paths and commit `feat: share scoped document read recovery`.

## Task 5: Retain source-aware web document placement and Back ownership

**Files:**
- Modify: `cmd/evener-hub/frontend/src/shell/workspace.ts`, `cmd/evener-hub/frontend/src/shell/DockHost.tsx`, `cmd/evener-hub/frontend/src/shell/mobile/StackHost.tsx`, `cmd/evener-hub/frontend/src/panes/doc/openDoc.ts`, `cmd/evener-hub/frontend/src/panes/session/transcript/fileOpenBeside.tsx`.
- Modify: `cmd/evener-hub/frontend/src/transcriptDisplay/renderContext.tsx`, `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptBody.tsx` and source-pane plumbing in `cmd/evener-hub/frontend/src/panes/session/Session.tsx` and `cmd/evener-hub/frontend/src/panes/transcript/Transcript.tsx`.
- Create: `cmd/evener-hub/frontend/src/shell/paneVisibility.tsx`.
- Test: `cmd/evener-hub/frontend/src/shell/workspace.test.ts`, `cmd/evener-hub/frontend/src/shell/DockHost.test.tsx`, `cmd/evener-hub/frontend/src/shell/mobile/StackHost.test.tsx`, `cmd/evener-hub/frontend/src/panes/session/transcript/fileOpenBeside.test.tsx`.

**Interfaces:**
- Consumes: `FileReference`, existing exact `OpenPaneRecord` identity and source `viewId` plumbing.
- Keeps dedup params `DocParams = {session: string; path: string; kind: "file" | "image"}`; binding/origin are retained state outside params.
- Adds the following opening contract in `panes/doc/openDoc.ts`:

```ts
export interface DocumentOpenRequest {
  readonly session: string;
  readonly reference: FileReference;
  readonly sourcePaneId: string;
}
export function openDocBeside(request: DocumentOpenRequest): void;
```

- Adds retained `DocumentPaneState = {reference: FileReference; origin: OpenPaneRecord | undefined; reopen: number}` associated with the exact document `OpenPaneRecord`, plus `documentPaneState(pane: OpenPaneRecord): DocumentPaneState | undefined` and a subscription through workspace updates.
- Adds `WorkspaceStoreState.promotePane(paneId: string): void`. It changes slots of existing records while preserving retained object identity and emits a new panes array for subscribers. Main's prior record becomes secondary; no record is deleted/reconstructed.
- Adds `sourcePaneId?: string` to the transcript render context. Only interactive owning session/transcript callers supply it. Missing context means no new filename action.
- Publishes actual pane visibility to descendants through new `PaneVisibilityContext`/`usePaneVisible()` in `shell/paneVisibility.tsx`. Desktop visibility is the dockview panel's visible state, not global focus; mobile visibility is its rendered foreground pane.

- [ ] **Step 1: Add red workspace and host tests.** Use the actual store and mounted hosts. Store-level core:

```ts
it("promotes the exact secondary opener and retains its parent", () => {
  const parentRef = "local:034MXwo6BpPH0QQCgdICSf";
  const childRef = "local:02wMz5TxvEMoJEDTDGOTil";
  const state = workspaceStore.getState();
  const parentId = state.openPane("session", { ref: parentRef });
  const sourceId = state.openPane("transcript", { ref: childRef, parentRef }, { slot: "secondary" });
  const parent = workspaceStore.getState().panes.find((pane) => pane.id === parentId);
  const source = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
  const reference = bindFilePath("docs/a.md", "/work/child");
  if (!reference || !parent || !source) throw new Error("fixture did not create bound panes");
  openDocBeside({ session: childRef, reference, sourcePaneId: sourceId });
  const panes = workspaceStore.getState().panes;
  expect(panes).toContain(parent);
  expect(panes).toContain(source);
  expect(source.slot).toBe("main");
  expect(parent.slot).toBe("secondary");
  const document = panes.find((pane) => pane.type === "doc");
  if (!document) throw new Error("document did not open");
  expect(documentPaneState(document)?.origin).toBe(source);
});
```

Reset store state between tests. Open aliases/location suffixes through the real action, assert one document and incremented reopen generation. Reuse from a second exact same-session pane, change focus, mount StackHost and assert Back returns to that second record. Close the source, reset/restore with potentially reused IDs, and assert no stale lifetime edge survives. Ordinary delegate opening must retain its prior placement policy.

Host tests must mount real conversation components with drafts/scroll state and prove promotion keeps both retained instances. Assert only two groups, active delegate plus document visibility, and no parent draft reset. Cover visible main while secondary owns focus, inactive secondary tab, and mobile foreground visibility.

- [ ] **Step 2: Run red.**

```bash
cd cmd/evener-hub/frontend
npm test -- src/shell/workspace.test.ts src/shell/DockHost.test.tsx src/shell/mobile/StackHost.test.tsx src/panes/session/transcript/fileOpenBeside.test.tsx
```

- [ ] **Step 3: Implement retained state and reconciliation.** Normalize with `bindFilePath` before opening. Migrate all existing tool-file/openDoc producers to a `DocumentOpenRequest`, preserving raw structured paths with spaces. Search `openDocBeside|fileDocParams` and update callers/tests together; do not add an old-signature fallback. Verify a supplied source pane is still a session/transcript about the requested session before recording it. Keep a missing source as the bound-session fallback, never globally focused ownership.

Store the latest exact origin on every explicit open, including reuse. Same binding increments `reopen`; changed binding replaces `reference` before focus/publication. Keep dedup params free of cwd, absolute target, source and generation. Remove retained state when its document closes/reset restores a different lifetime. An initially restored document without in-memory binding must hydrate its own session before creating a fresh relative binding; it may not fetch with a guessed focused cwd.

```ts
// promotePane's slot update preserves the records used as retained-origin keys.
const source = panes.find((pane) => pane.id === paneId);
if (!source) return;
for (const pane of panes) {
  if (pane === source) pane.slot = "main";
  else if (pane.slot === "main") pane.slot = "secondary";
}
// Publish a new panes array through the existing store update, retaining its records.
```

In DockHost reconcile slot changes by moving existing dockview panels/groups, not removing/readding them. Verify the installed pinned dockview move/visibility methods from its type declarations before use. Add no third group. Subscribe to that panel's visibility event through the verified API, drive the visibility context with its actual visible state, and dispose the listener. In StackHost, consult the retained document origin before transient local back-stack fallback, including when the host remounts after breakpoint change. If that origin is closed, navigate to the document's bound session through the existing action.

- [ ] **Step 4: Run green and typecheck.** Run the Step 2 command, `npm run typecheck`, and existing workspace/mobile navigation suites. Format the exact touched files with frontend Biome.

- [ ] **Step 5: Review and commit.** Stage only this unit's paths and commit `feat: retain source-aware document pane ownership`.

## Task 6: Recognize assistant filenames through a reversible web pipeline

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/AgentMarkdown.tsx`, `cmd/evener-hub/frontend/src/panes/session/transcript/EntityText.tsx`.
- Create: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/fileReferenceDOM.ts`.
- Test: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/agentFileLinks.test.tsx`, `cmd/evener-hub/frontend/src/panes/session/transcript/messages/agentEntityLinks.test.tsx`, `cmd/evener-hub/frontend/src/panes/session/transcript/messages/agentMarkdownDiagramWalk.test.tsx`.

**Interfaces:**
- Consumes: shared parse/find functions, owning `{sessionRef, cwd, sourcePaneId}`, Task 5's `openDocBeside` and existing safe `docFileRawURL`/image URL builders.
- Adds `enhanceFileReferences(root: HTMLElement, context: {sessionRef: string; cwd: string; sourcePaneId: string}): () => void` in the new DOM adapter.
- Extracts existing entity enhancement into `enhanceEntityText(root: HTMLElement): {portals: ReactNode[]; cleanup(): void}`; keep `useEntityTextEnhancement` for existing callers rather than copying entity logic.

- [ ] **Step 1: Add red integration tests using actual AgentMarkdown/entity rendering.** Start with `agentFileLinks.test.tsx`'s `message(markdown, live, snapshot)` and complete thread fixtures. Render Jesse's examples, click each filename, and assert source-bound doc state and zero document fetches before clicks. Add missing cwd then hydration with unchanged text, StrictMode, streaming/settled updates and changed session/cwd/source-pane context.

```tsx
test("hydration recognizes the whole file before enhancing its delegate substring", () => {
  const sourcePaneId = workspaceStore.getState().openPane("session", { ref: thread.ref });
  const filename = "reports/dlg_02wMz5TxvEMoJEDTDGOTil.md";
  const body = `${filename} and dlg_02wMz5TxvEMoJEDTDGOTil`;
  const show = (snapshot: ThreadModel) => (
    <TranscriptRenderProvider thread={snapshot} sourcePaneId={sourcePaneId}>
      <AgentMessageItem
        item={{ id: "message", turnId: "turn", type: "agentMessage", text: body, pendingText: [body] }}
        turn={{ id: "turn", status: "completed", items: [] }}
        sessionRef={snapshot.ref}
        live={false}
      />
    </TranscriptRenderProvider>
  );
  const { container, rerender, unmount } = render(<StrictMode>{show({ ...thread, cwd: "" })}</StrictMode>);
  expect(screen.queryByRole("link", { name: filename })).toBeNull();
  rerender(<StrictMode>{show(thread)}</StrictMode>);
  const link = screen.getByRole("link", { name: filename });
  expect(link.querySelector("[data-entity-host]")).toBeNull();
  expect(container.querySelectorAll("[data-entity-host]")).toHaveLength(1);
  expect(container.querySelector("[data-entity-host]")?.textContent).toContain("dlg_02wMz5TxvEMoJEDTDGOTil");
  rerender(<StrictMode>{show({ ...thread, cwd: "/workspace/other" })}</StrictMode>);
  expect(screen.getAllByRole("link", { name: filename })).toHaveLength(1);
  const bubble = screen.getByTestId("agent-bubble");
  unmount();
  expect(bubble.querySelector("[data-entity-host]")).toBeNull();
  expect(bubble.textContent?.trimEnd()).toBe(body);
});
```

This uses Task 5's new provider `sourcePaneId` prop and the existing complete `thread` fixture, `AgentMessageItem`, StrictMode and entity host marker. Keep the actual entity hook. Add a resolved standalone delegate control using the real activity/entity-index fixture as well as this unresolved marker case. Add invalid full-token boundaries across formatting: `../**docs/a.md**`, `https://host/**docs/a.md**`, adjacent inline punctuation and a split candidate. Cover explicit encoded destinations, external anchors, modified click, keyboard activation, code/Mermaid/entity exclusions and sanitizer regressions. Existing remote-ref tests that expected unchanged text now become valid remote-opening tests; retain invalid-ref rejection.

- [ ] **Step 2: Run red.**

```bash
cd cmd/evener-hub/frontend
npm test -- src/panes/session/transcript/messages/agentFileLinks.test.tsx src/panes/session/transcript/messages/agentEntityLinks.test.tsx src/panes/session/transcript/messages/agentMarkdownDiagramWalk.test.tsx
```

- [ ] **Step 3: Implement file-first/entity-second enhancement with one lifetime.** Build eligible block text with offsets back to DOM text nodes. Inline formatting edges do not delimit tokens; represent excluded anchors/code blocks/Mermaid/entity controls as barriers. Recognize an entire inline code element only, leaving ambiguous multi-node path spans unchanged rather than linking a suffix.

```ts
// The one AgentMarkdown layout-effect lifetime owns both passes.
const restoreFiles = enhanceFileReferences(rootElement, context);
const entityResult = enhanceEntityText(rootElement);
// Publish entityResult.portals through the existing portal state.
return () => {
  entityResult.cleanup();
  restoreFiles();
};
```

`enhanceEntityText` is the extracted existing implementation, with its return shape explicitly `{portals, cleanup}`. The effect invalidates on `source`, `live`, session ref, cwd and source-pane ID. Cleanup runs reverse order and checks the DOM epoch it owns before touching nodes, so old cleanup cannot restore over a newer React render.

Generated anchors keep original visible spelling and inline-code styling. Primary unmodified activation prevents default/propagation and opens the captured `DocumentOpenRequest`; modified clicks keep the existing safe hub document href mode, replacing only its session/path with the bound source and absolute target. Do not change an HTML-viewer href to the raw-text URL. Explicit file links retain `OpenButton`. Generated words use their link action without an extra button. Validate source refs with the existing shared transcript-ref parser, including host-qualified refs. Do not alter common Markdown sanitization, rewrite recorded text, scan user/tool/delegate-report renderers, or issue existence requests.

- [ ] **Step 4: Run green.** Run Step 2, existing disclosure/Markdown suites and `npm run typecheck`; format the touched adapter/component/tests. Confirm DOM text restoration and one listener/action per reference under StrictMode.

- [ ] **Step 5: Review and commit.** Commit exact changed paths as `feat: open assistant filenames from web replies`.

## Task 7: Bind web viewer reads to session publication and visible recovery

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/doc/DocPane.tsx`, existing `cmd/evener-hub/frontend/src/panes/doc/DocPane.test.tsx`.
- Create: `cmd/evener-hub/frontend/src/panes/doc/useDocumentRead.ts`, `cmd/evener-hub/frontend/src/panes/doc/useDocumentRead.test.tsx`.
- Modify: `cmd/evener-hub/frontend/src/panes/doc/openDoc.ts` only if needed to expose the retained state defined in Task 5.

**Interfaces:**
- Consumes: retained `DocumentPaneState`, shared `FileReference`, `rebindFileReference`, `DocumentReadDemand`, `DocumentReadAttempt`, `docImageReadURL`, existing `threadsStore`/thread subscription and connection state.
- Adds the following viewer-local hook contract. Its optional port defaults to existing `browserDocPort`; tests can supply a real temporary HTTP port. Read identity includes that port's origin. No global document cache.

```ts
export interface DocumentReadState {
  readonly content: DocFileContent | undefined;
  readonly errorKind: DocFileErrorKind | undefined;
  readonly notice: string | undefined;
  readonly reference: FileReference;
  readonly imageGeneration: string | undefined;
  reload(): void;
}
export function useDocumentRead(
  session: string,
  reference: FileReference,
  reopen: number,
  visible: boolean,
  port?: DocPort,
): DocumentReadState;
```

- [ ] **Step 1: Add red mounted-viewer tests.** Use real DocPane with its actual retained workspace state. Transport deferred responses are the HTTP boundary. Assert normalized display path stays separate from requested absolute target. Deferred A after cwd B cannot publish. Clear A at acknowledged B even when B's first read fails; relative references request B, while absolute A remains A and becomes forbidden. Reopening with B explicitly replaces either binding.

```ts
// In useDocumentRead.test.tsx, use Node's real HTTP server and React renderHook.
import { createServer } from "node:http";
import { once } from "node:events";
import { act, renderHook, waitFor } from "@testing-library/react";

test("replacement clears A even when B fails and late A arrives", async () => {
  const a = bindFilePath("plan.md", "/work/a");
  const b = bindFilePath("plan.md", "/work/b");
  if (!a || !b) throw new Error("expected bound references");
  let aReads = 0;
  let releaseA: (() => void) | undefined;
  let healthyB = false;
  const server = createServer((request, response) => {
    const target = new URL(request.url ?? "", "http://fixture").searchParams.get("path");
    response.setHeader("Content-Type", "text/plain");
    if (target === "/work/a/plan.md") {
      aReads += 1;
      if (aReads === 1) response.end("A bytes");
      else releaseA = () => {
        if (!response.destroyed && !response.writableEnded) response.end("late A bytes");
      };
    } else if (target === "/work/b/plan.md") {
      response.statusCode = healthyB ? 200 : 503;
      response.end(healthyB ? "B bytes" : "attachment unavailable");
    } else {
      response.statusCode = 404;
      response.end("unexpected target");
    }
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("expected TCP fixture");
  const port: DocPort = { origin: `http://127.0.0.1:${address.port}`, fetch };
  const view = renderHook(({ reference }) => useDocumentRead(thread.ref, reference, 0, true, port), {
    initialProps: { reference: a },
  });
  try {
    await waitFor(() => expect(view.result.current.content?.text).toBe("A bytes"));
    act(() => view.result.current.reload());
    await waitFor(() => expect(releaseA).toBeTypeOf("function"));
    view.rerender({ reference: b });
    expect(view.result.current.content).toBeUndefined();
    if (!releaseA) throw new Error("old read did not reach HTTP fixture");
    releaseA();
    await waitFor(() => expect(view.result.current.errorKind).toBe("error"));
    expect(view.result.current.content).toBeUndefined();
    healthyB = true;
    act(() => view.result.current.reload());
    await waitFor(() => expect(view.result.current.content?.text).toBe("B bytes"));
  } finally {
    view.unmount();
    releaseA?.();
    server.closeAllConnections();
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  }
});
```

Reuse the complete valid `thread` fixture from the web transcript tests; define it in this hook test's local fixture rather than importing another test file. The hook test pins read-identity replacement through the real shared HTTP reader. Add the mounted DocPane/owning-thread-publication version separately to exercise the actual lease and rendering. Cover same-identity failure retaining content plus explanation; typed 403/404/501 stopping timers; continuously-ready remote attachment recovery; owning vs unrelated connection events; active secondary tab/main visibility, hide/background/resume/close; port-origin replacement; actual Reload action and missing-file-created recovery. Keep binary, truncation, sanitized Markdown and cap tests.

- [ ] **Step 2: Run red.**

```bash
cd cmd/evener-hub/frontend
npm test -- src/panes/doc/DocPane.test.tsx src/panes/doc/useDocumentRead.test.tsx
```

- [ ] **Step 3: Implement one owning-session lease and generation-guarded reads.** Use existing thread hydration/subscription facilities for the viewer's exact session, not focused navigation rows. Dispose the lease and browser foreground/connection listeners on close. Observe published cwd in the same session model. At a changed cwd, replace the reference explicitly, clear content synchronously, then call demand replacement.

```ts
const replacement = rebindFileReference(currentReference, publishedCWD);
// In one viewer state update: store replacement and clear prior content/error/image.
// Then retire the demand's previous epoch and queue the replacement read.
demand.replace();
```

The read closure snapshots session, origin and reference at attempt start. It requests `reference.readTarget` through existing `readDocFile`, and publishes only when `attempt.isCurrent()` and the captured identity still matches. Associate stored content with its identity and suppress it during rendering whenever the current identity differs, including before an effect runs after a port/route change. Map 403/404/501 to terminal and transport/generic 503 failures to transient. Same-identity refresh preserves healthy content and adds the actual failure explanation. Reload lives in the existing pane action surface; it refreshes the captured identity, never invents a cwd.

For images, start a generation-tagged authenticated load as that scheduler attempt. Resolve on load/error even for retired generations; only current generations may update the UI. Keep the healthy displayed image while a replacement preload is pending, swap only on successful current load, and preserve old image plus failure explanation on same-identity failure. Replacement clears it immediately. Hiding must let the old attempt settle without publishing so pending demand can resume single-flight. Closing retires publication and timers, releases handlers and settles the abandoned attempt; no closed viewer starts another read. Normal transcript-image previews still use their old URL helper.

- [ ] **Step 4: Run green and frontend gate.** Format touched files, run Step 2 and then from repository root:

```bash
make test-web
```

Expected: unit tests, typecheck and enforced Biome pass. Report the gate's actual work, not an inferred test count.

- [ ] **Step 5: Review and commit.** Commit this unit as `feat: recover source-bound web document reads`.

## Task 8: Make native Reader source-bound and recoverable

**Files:**
- Modify: `mobile-native/src/reader/documentSource.ts`, `mobile-native/src/reader/documentSource.test.ts`, `mobile-native/src/reader/useDocument.ts`, `mobile-native/src/reader/useDocument.test.tsx`, `mobile-native/src/reader/ReaderScreen.tsx`, `mobile-native/src/reader/ReaderScreen.test.tsx`.
- Modify: `mobile-native/src/screens.tsx` and existing Reader navigation producers/tests discovered with `rg 'navigate\("Reader"|Routes.*Reader|nativeDocImageSource' mobile-native/src`.
- Keep viewer image-generation changes in `mobile-native/src/reader/ReaderScreen.tsx`; leave ordinary preview helpers unchanged.

**Interfaces:**
- Consumes: shared binding/demand/image APIs; existing `DocPort`, `SessionLink`, `useScreenInFront`, `AppState` and connection profile/ready state.
- Changes Reader route params to require `reference: FileReference` in addition to existing `{hubId, sessionRef, path, sessionTitle, updatedAt?}`. `path` remains normalized display identity; reads use `reference.readTarget`.
- Changes `useDocument(hubId: string, sessionRef: string, reference: FileReference, inFront: boolean)` to return `{document: LoadedDocument | null; notice: string | undefined; reference: FileReference; imageGeneration: string | undefined; reload(): void}`. `LoadedDocument` is the existing union in `documentSource.ts`; keep the platform-local representation and scroll/review memory.
- Replaces native's remote `elsewhere` refusal with transport-derived `host-unsupported`; no old-state fallback.

- [ ] **Step 1: Add red native transport and Reader tests.** `loadDocument` must call the real shared reader for a host-qualified ref. Use real local HTTP servers for route/error assertions, not canned document-loader returns. Assert distinct forbidden/missing/host-unsupported/transient outcomes, binary/cap/truncation and Markdown content. Existing image classification remains.

```ts
import { createServer } from "node:http";
import { once } from "node:events";

it("lets remote text reach transport and preserves unsupported-host errors", async () => {
  const requests: string[] = [];
  const server = createServer((request, response) => {
    requests.push(request.url ?? "");
    response.statusCode = 501;
    response.end("host does not support document reads");
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  try {
    const address = server.address();
    if (!address || typeof address === "string") throw new Error("expected TCP fixture");
    const port: DocPort = { origin: `http://127.0.0.1:${address.port}`, fetch };
    const result = await loadDocument(port, "h1:local:02wMz5Txv1C3Hut0M8GCeB", "/work/b/docs/a.md");
    expect(result.kind).toBe("host-unsupported");
    expect(requests).toHaveLength(1);
    const url = new URL(requests[0] ?? "", port.origin);
    expect(url.searchParams.get("session")).toBe("h1:local:02wMz5Txv1C3Hut0M8GCeB");
    expect(url.searchParams.get("path")).toBe("/work/b/docs/a.md");
  } finally {
    server.closeAllConnections();
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  }
});
```

Repeat this HTTP transport test with 200 actual text, 403, 404 and 503; the new fixture supplies HTTP status only, while the real shared reader and native loader do the classification. Mounted Reader tests capture the real Document actions items, keep the screen mounted after missing, create the fixture file, activate its actual `Reload` item and assert useful text. Deferred A/B tests must clear A on cwd/origin replacement, reject old results and retain healthy content only for same-identity failures. Drive the existing SessionLink producer to B; test relative and absolute transitions separately. Keep controller ready while the owning attachment fails/revives; fake only the clock/transport boundary. Other profile's ready event must cause no read.

- [ ] **Step 2: Run red.**

```bash
cd mobile-native
npm test -- src/reader/documentSource.test.ts src/reader/useDocument.test.tsx src/reader/ReaderScreen.test.tsx
```

- [ ] **Step 3: Implement source-scoped Reader lifetime.** Remove the remote-ref preflight. Preserve typed `host-unsupported` from shared transport and show an unsupported-host explanation; 503 remains transient. Migrate every Reader producer to capture a `FileReference` from that source's current cwd, including Files/artifact actions with raw-space paths. Missing cwd waits for that owning session's hydration, not another selected conversation.

Bind HTTP origin/credentials to `profiles.find((profile) => profile.id === hubId)`. Guard connection and SessionLink activity with `activeProfile?.id === hubId`; never invent per-hub connection APIs. Reuse Reader's existing SessionLink lease for status and cwd rather than creating a second subscription. Reader owns the current reference initialized from the route, replaces it on that lease's published cwd, and passes it to `useDocument`. Associate loaded content with its complete identity and render it only for that identity, including before route/origin-change effects run. Pass `useScreenInFront(route.key)` and AppState foreground into scheduler activity. Origin replacement retires old publication and clears content. Preserve drafts/comments/scroll for retained same identity; do not relabel old bytes under a new title.

```ts
// Reader's existing Document actions item list, with reload in its memo dependencies.
{ type: "action", label: "Reload", onPress: reload }
```

For native Image, use a generation-specific `docImageReadURL` in the existing authenticated source with the same token headers. Wire load/error events to the exact scheduler attempt and captured source; a text reload alone is insufficient. Keep healthy same-identity images during refresh and clear replacement images. Handle untyped image errors as bounded transient failures with honest unavailable copy. Do not change global preview helpers or add a retry toolbar. Update Reader's stale comment forbidding refresh controls to describe the existing menu action.

- [ ] **Step 4: Run green and native checks.**

```bash
cd mobile-native
npm test -- src/reader/documentSource.test.ts src/reader/useDocument.test.tsx src/reader/ReaderScreen.test.tsx
npm run check
npm run check:scripts
```

Format touched native TypeScript with the native tree's own Biome. Assert no timers/subscriptions survive route disposal.

- [ ] **Step 5: Review and commit.** Commit exact changed native paths as `feat: recover source-bound native Reader documents`.

## Task 9: Open native assistant filenames and align document chips

**Files:**
- Create: `mobile-native/src/reader/markdownFileReferences.ts`, `mobile-native/src/reader/markdownFileReferences.test.ts`.
- Modify: `mobile-native/src/MarkdownResponse.tsx`, `mobile-native/src/MarkdownResponse.test.tsx`, `mobile-native/src/TimelineItem.tsx`, `mobile-native/src/screens.tsx`, `mobile-native/src/reader/documentReferences.ts`, `mobile-native/src/reader/documentReferences.test.ts`.

**Interfaces:**
- Consumes: shared FileReference parsing and Task 8's Reader route.

```ts
export interface NativeFileOpenContext {
  readonly cwd: string;
  openFile(reference: FileReference): void;
}
export interface MarkdownFileRender {
  readonly markdown: string;
  readonly references: ReadonlyMap<string, FileReference>;
}
export function renderMarkdownFileReferences(markdown: string, cwd: string): MarkdownFileRender;
export function markdownFileReferences(markdown: string, cwd: string): Array<{
  reference: FileReference;
  surface: "prose" | "code" | "link";
}>;
```

`NativeFileOpenContext.openFile` is created in the owning conversation and captures `{hubId, sessionRef, sessionTitle}`. Only the assistant message case receives it. Generated destinations are per-render identifiers looked up in `references`; they are never navigated as URLs. The discovery function shares the token adapter, not a second recognizer. Preserve the existing bounded content-token cache; cache no cwd-bound action or destination.

- [ ] **Step 1: Add red parser-output, event-wiring and discovery tests.** Cover Jesse's examples, backticked basenames/extensionless paths, explicit/reference links, Unicode, escapes, entity decoding and percent order, adjacent invalid formatting, indented/fenced code and Mermaid. Compare destination `readTarget`/`path` to independently written expected values.

```ts
it("generates source-bound native file actions", () => {
  const original = "Spec: docs/plan.md\n\n`README.md` and [R](./docs/a%26b.md)";
  const result = renderMarkdownFileReferences(original, "/work/b");
  expect([...result.references.values()].map((ref) => ref.path)).toEqual(["docs/plan.md", "README.md", "docs/a&b.md"]);
  expect([...result.references.values()].map((ref) => ref.cwd)).toEqual(["/work/b", "/work/b", "/work/b"]);
});
```

Mount actual MarkdownResponse/TimelineItem with a source-bound context, invoke the renderer's real event-prop path and assert Reader navigation carries that context's hub/session and reference, regardless of selected conversation. Such invocation proves wiring only; Task 10 proves actual parser destination generation. Assert unrecognized identifiers never open files, external links retain existing actions, recognized files never reach `Linking.openURL`, recognized emitted long-press offers Open file/Copy path, and ordinary response holds/copy/selectability remain unchanged. Stream text then replace cwd/context; old render identifiers no longer open.

Discovery tests prove prose and inline actions agree on normalized destination; bare chip names still need successful write evidence, inline backticked actions do not. Preserve write ages and file/artifact list tests.

- [ ] **Step 2: Run red.**

```bash
cd mobile-native
npm test -- src/reader/markdownFileReferences.test.ts src/MarkdownResponse.test.tsx src/reader/documentReferences.test.ts
```

- [ ] **Step 3: Implement token/source-span adaptation.** Use installed `marked` lexer token `raw` spans to walk eligible blocks and inline tokens. Gather whole-block boundary context before considering individual text/code spans. Existing anchors are excluded from prose scanning. Resolve reference definitions for link targets, preserve raw Markdown source outside generated ranges, and apply replacements right-to-left. Do not reserialize the entire Markdown AST or regex-rewrite fences/reference definitions.

Keep `splitNativeSegments` before adapting non-Mermaid segments. Decode Markdown link escaping/entities once using the adapter's destination representation before shared URI parsing; do not decode prose/code percent text. Native pinned parser entity behavior is qualified in Task 10. Generate per-render opaque destinations and keep their FileReferences in the result map.

```ts
// Inside the component's existing onLinkPress event handler:
const reference = fileRender.references.get(event.url);
if (reference && fileContext) {
  fileContext.openFile(reference);
  return;
}
// Continue through the existing external-link handler for non-file destinations.
```

Use the renderer's actual event shape from installed types, not this fragment's variable name as an API claim. Unknown generated-identifier-shaped destinations are rejected, never handed to a browser. Source callbacks capture their conversation. Keep the original `markdown` for Copy response, accessibility and outer message actions. Align chip discovery through `markdownFileReferences`, preserving its separate basename/write policy and existing bounded token cache.

- [ ] **Step 4: Run green and checks.** Run Step 2, native timeline/navigation tests, `npm run check` and `npm run check:scripts`. Format exact touched paths with native Biome.

- [ ] **Step 5: Review and commit.** Commit exact changed files as `feat: open assistant filenames through native Markdown`.

## Task 10: Qualify real geometry, cache and native parsing, then finish the requested PR

**Files:**
- Create: `cmd/evener-hub/doc_file_links_browser_test.go`.
- Create: `cmd/evener-hub/frontend/src/dev/DocumentFileLinksFixture.tsx` for real shell/assistant components, not duplicated markup.
- Create: `cmd/evener-hub/frontend/src/dev/documentfilelinksharness-entry.tsx`, `cmd/evener-hub/frontend/documentfilelinksharness.html`, `cmd/evener-hub/frontend/scripts/documentfilelinksguard/run.mjs`.
- Modify: `cmd/evener-hub/frontend/package.json`, `scripts/web/test-web-browser.sh`, `make/testing.mk` to include the new opt-in browser guard.
- Create: `mobile-native/scripts/markdown-file-links-probe.cpp`, `mobile-native/scripts/check-markdown-file-links.mts`.
- Create: `docs/product/documents.md`.
- Modify: `docs/product/README.md`, affected S02/S03/S05/S06/S13 rows in `docs/product/subsystems.md`, `docs/web-ui/design-system.md`, `mobile-native/README.md`.
- Recheck: `appwire-client/typescript/README.md` exports/recovery documentation from Tasks 3 and 4.

**Interfaces:**
- Consumes: the actual production adapters, workspace hosts, viewers and Task 2's temporary HTTP/worktree fixture. No new product protocol or capability.
- Produces: deterministic browser qualification integrated with existing browser guards; a named pinned-native-parser qualification command; evergreen ownership docs; a reviewed feature PR shepherded to authorized merge conditions.

- [ ] **Step 1: Write failing real-browser scenarios before changing the harness.** Follow `cmd/evener-hub/app_retirement_browser_test.go` and its real CDP runner, serving real temporary files with the actual document routes. The new Go test defines `flag.Bool("document-file-links-browser", false, "run isolated document file-links browser fixture")`, skips ordinary Go gates without that explicit opt-in, and launches the named Node guard with fixture URLs and an artifact directory. The new npm `documentfilelinksguard` script invokes the exact opt-in Go command in Step 3. Default tests never depend on Chrome availability.

Use production AgentMarkdown, DockHost, StackHost and DocPane via the new dev fixture. Its pane wrappers provide `data-file-links-source` and `data-file-links-document` on elements occupying those actual panes. Import the existing `evaluate` helper from `cmd/evener-hub/frontend/scripts/browserGuardCdp.mjs` and use Node's assertion API, not an added Playwright dependency. Browser geometry assertions include:

```js
import assert from "node:assert/strict";
const geometry = await evaluate(send, `(() => {
  const source = document.querySelector('[data-file-links-source]');
  const viewer = document.querySelector('[data-file-links-document]');
  if (!source || !viewer) throw new Error('source or document is missing');
  const a = source.getBoundingClientRect();
  const b = viewer.getBoundingClientRect();
  return { sourceRight: a.right, documentLeft: b.left, sourceWidth: a.width, documentWidth: b.width };
})()`);
assert(geometry.sourceWidth > 0 && geometry.documentWidth > 0);
assert(geometry.documentLeft >= geometry.sourceRight);
```

Reuse `startBrowserGuard`, `connectPage`, `navigateTo`, `applyViewport` and `evaluate` from the existing runner. The Go fixture passes its hub/document URLs from Task 2 and test-only mutation controls implemented on that fixture server, not on production routes. Test both exact example links, narrow wrapping, keyboard activation/focus, desktop source/document geometry, phone full-screen and Back, secondary delegate promotion with parent draft/scroll retained, same-document latest opener, closed-source fallback and desktop→phone→desktop host changes. Assert no third column or document duplication.

For HTTP image cache proof, write a real red image, open it, replace it with a blue image at the same path inside 60 seconds, explicitly reopen and Reload, and inspect loaded image pixels/bytes. Then fail an image request, restore it while the controller stays ready, and verify capped recovery. Keep one viewer mounted across a same-path worktree switch. A URL inequality assertion alone is insufficient; verify the browser received the changed image. Leave transcript preview cache assertions unchanged.

- [ ] **Step 2: Add and run the actual native parser qualification.** The probe compiles the installed lock-matched `react-native-enriched-markdown@1.0.2` C++/MD4C parser sources without modifying them. Read the installed source layout and existing probe/parser constructors before setting compiler inputs; do not guess package paths or native ABI. `check-markdown-file-links.mts` loads production `renderMarkdownFileReferences`, sends its output to the compiled probe, obtains link destinations and resolves them through the production per-render map. Fail on a missing compiler/package/parser or unexpected version rather than printing an unearned pass.

```ts
// Qualification corpus, with explicit expected filesystem meaning.
const corpus = [
  { markdown: "[R](./docs/a&amp;b.md)", path: "docs/a&b.md" },
  { markdown: "[R](./docs/a%26b.md)", path: "docs/a&b.md" },
  { markdown: "[R](./docs/a%3A12%23L4%3F.md)", path: "docs/a:12#L4?.md" },
  { markdown: "`reports/dlg_02wMz5TxvEMoJEDTDGOTil.md`", path: "reports/dlg_02wMz5TxvEMoJEDTDGOTil.md" },
];
```

Also cover escaped punctuation, reference definitions, entity-looking filename text, literal percent data and invalid traversal across formatting. Compare with the actual sanitized web DOM anchor representation through the same shared parser. Document the named command:

```bash
cd mobile-native
npx tsx scripts/check-markdown-file-links.mts
```

The prior review's parser probe is supporting evidence only; qualification must run current production adapter output. Parser results and callback tests prove destination/event construction, not UIKit touch arbitration or native image cache behavior. If simulator/device access exists, check ordinary response holds, generated/explicit/external taps, emitted file holds, Copy response, accessibility, Reader Back and image freshness. If unavailable, name every unqualified item in the PR/final report and do not claim those checks passed.

- [ ] **Step 3: Run gates and fix failures at their source.** After formatting all touched TypeScript from the correct owning directory:

```bash
make test-web
make test-web-browser
make test-api-package
go test ./cmd/evener-hub -run '^TestDocumentFileLinksBrowser$' -count=1 -args -document-file-links-browser
cd mobile-native && npm test -- src/reader src/MarkdownResponse.test.tsx && npm run check && npm run check:scripts
```

Run from repository root except the explicitly native command. The new flag above is defined by this task, not an existing Evener option. Re-run affected Go tests from Tasks 1 and 2. The standalone new browser command is useful for red/green debugging; the final `make test-web-browser` gate includes it, so do not rerun it redundantly after a verified gate. Do not claim a skipped browser case ran. Let CI run full lint/vet/test/race lanes; do not delay a push for an unnecessary full workstation suite.

- [ ] **Step 4: Write evergreen behavior docs and commit qualification.** `docs/product/documents.md` owns source binding, current-worktree semantics, fresh live/archive root authority, relative/absolute cwd transitions, visibility/foreground/source-scoped recovery and terminal/transient distinctions, image generations, latest exact Back ownership, and platform qualification limits. Link it from product README and all affected subsystem rows. Update design-system filename actions and native Reader/Reload/assistant-only behavior. Keep historical review/rollout details out of evergreen guides.

Stage only this task's changed fixture/harness/docs files, inspect the staged diff, and commit `test: qualify worktree filename opening across clients`.

- [ ] **Step 5: Run the requested four-reviewer simplify-code workflow.** Load `/simplify-code:simplify-code` and follow its installed four-reviewer process. Give reviewers the approved spec, this plan, the full branch diff and recorded red/green evidence. Read all reports, reconcile disagreements, make only behavior-preserving cleanups and rerun affected checks. Correctness findings require a failing behavior test before a fix. Commit cleanup separately with exact staged paths. Do not lower coverage or substitute a stubbed test for real producer/browser/native-parser evidence.

- [ ] **Step 6: Create and shepherd the feature PR.** Load `/shepherd-pr:shepherd-pr`. Review the complete branch diff against the spec, push this feature branch, and create the PR with source/identity/root guarantees, test commands/results and unavailable native qualification stated explicitly. Follow one complete edit round, one push, one CI/review run and one settle-detector wait. Run the detector as its own background job. Read current-head RoboRev's combined body and this checkout's open per-commit reviews; green review execution alone is not a clean review.

Merge the latest base before requesting approval, following repository ref/uncommitted-change safeguards. Fix genuine findings red-test-first, refute incorrect findings with exact code/check evidence, and never blanket-close other lanes' reviews. Merge only under the repository's authorized green-CI/current-head-review conditions. No deploy/restart follows the merge.

## Self-review and handoff record

Coverage map: spec intent/out-of-scope → Tasks 3, 6 and 9; installed/root authority → Tasks 1 and 2; grammar/binding/URI order → Task 3; retained web placement/Back → Task 5; reversible/entity pipeline → Task 6; web/native identity/recovery/images → Tasks 4, 7 and 8; native Markdown/chips → Task 9; actual geometry/cache/parser and evergreen docs → Task 10; requested simplify/PR shepherd → Task 10.

Self-review checked spec coverage, placeholder patterns, repeated interface names/types and all five Review Focus classes. It corrected existing-file paths, bounded the carrier test's failure wait, specified the new browser flag/runner, made hook return types explicit and added real HTTP test examples. A repository-path scan and `git diff --cached --check` validate the document; they are not product tests. No feature tests or dependency installs ran during planning.

Pinned dockview method declarations and native parser compiler inputs remain execution-time inspections because this worktree's dependencies are not installed. Their owning tasks require that inspection before use. Execution may refine those details, but it may not change approved product semantics, introduce backward-compatibility behavior, or claim unavailable qualification. Ask Jesse before a consequential scope or architecture change.

After docs-only commit, ask Jesse to review this plan and choose Subagent-driven or Native execution. Stop until that answer arrives. No implementation, test execution or dependency installation is authorized by writing this plan.
