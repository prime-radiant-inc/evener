# Full suite fix report — delegate-send-always-wakes

Date: 2026-08-04
Branch: `delegate-send-always-wakes`
Final commit: `3b55858c9`

## Scope

Investigated and fixed the two fresh `go test ./...` failures called out for this branch:

1. `cmd/serf-hub`:
   - `TestWeb_APITreeLiveRowsCarryTierFavoriteRename`
   - `TestWeb_APITreeOrphanLiveRowsCarryTierFavoriteRename`
2. `internal/appwirets`:
   - `TestGeneratedFileCurrent`

I preserved the delegate-send work on this branch and changed only the files needed to fix the proven regressions and the broken test harness assertion.

## Root causes

### 1) `cmd/serf-hub`: live/orphan rows lost `Favorite=true`

**Observed failure**
- The reported failures expected session rows to carry `Favorite=true`, but the rows were rendered with `Favorite=false`.

**Minimal proof**
- In `cmd/serf-hub/web_api_tree.go`, `handleAPITree` computed:

```go
revalidation := hubcore.ClassifyFavoriteDecisions(decisions, authority)
```

- But it then narrowed the map before rendering rows:

```go
favs := projectFavoritePresentation(revalidation.Presentation)
```

- `apiTreeNodeTier` reads **session** favorites from:

```go
out.Favorite = favs[hubcore.ArchiveKey{Kind: "session", ID: n.ID}]
```

- `projectFavoritePresentation(...)` keeps only `kind == "project"` entries, so every session lookup returns `false`.

**Comparison to earlier/base behavior**
- `git show 26d8abc0335470da690f1bbd26256733347cc2a0:cmd/serf-hub/web_api_tree.go` showed the pre-regression code passed the full classification presentation directly:

```go
favs := hubcore.ClassifyFavoriteDecisions(decisions, authority).Presentation
```

- `git blame` traced the narrowing line and helper to commit `b417a8bfe` (`feat(hub): project named pin sections in navigation`).

**Conclusion**
- The regression was introduced when the request-scoped favorite map was narrowed to project-only favorites and then reused for session row rendering.

### 2) `internal/appwirets`: `types.gen.ts` was stale

**Observed failure**
- `TestGeneratedFileCurrent` compares `EmitCatalog()` against `cmd/serf-hub/frontend/src/protocol/types.gen.ts` and reported the committed generated file stale.

**Minimal proof**
- `internal/appwirets/rawFieldsOf` emits struct fields in **Go declaration order**.
- Current `appwire/types.go` declares `JobActivityJob` as:

```go
type JobActivityJob struct {
    JobID          string `json:"jobId"`
    OwnerSessionID string `json:"ownerSessionId"`
    OwnerRef       string `json:"ownerRef"`
    TranscriptRef  string `json:"transcriptRef,omitempty"`
    Type           string `json:"type"`
    Status         string `json:"status"`
    Outcome        string `json:"outcome,omitempty"`
    ...
}
```

- The committed `cmd/serf-hub/frontend/src/protocol/types.gen.ts` instead had `transcriptRef?: string;` **after** `outcome?: string;`.
- History proved this was not a generator bug:
  - `git show 8717f8495:appwire/types.go` showed the original `JobActivityJob` shape before `TranscriptRef` existed.
  - `git log -S 'TranscriptRef  string `json:"transcriptRef,omitempty"`' -- appwire/types.go` identified commit `bace97fed` (`feat: expose transcript refs in activity jobs`) as the appwire change.
  - `git show 417a6238d -- cmd/serf-hub/frontend/src/protocol/types.gen.ts` showed the generated file was updated later by adding `transcriptRef?: string;`, but in the wrong position relative to Go field order.
  - No later commit touched `appwire/types.go`, `internal/appwirets`, or `types.gen.ts` to reconcile that mismatch.

**Conclusion**
- The generator workflow (`appwire/doc.go` → `go:generate go run primeradiant.com/serf/internal/appwirets -out ../cmd/serf-hub/frontend/src/protocol/types.gen.ts`) was not reflected in the committed file. The stale file needed regeneration-equivalent update; the generator itself did not need code changes.

### 3) Additional proven issue while verifying: orphan-live pin assertion lacked a pin store

**Observed during verification**
- After fixing the real `Favorite` regression, `TestWeb_APITreeOrphanLiveRowsCarryTierFavoriteRename` still failed on:

```text
orphan-live favorite pin = [], want the favorited orphan
```

**Minimal proof**
- The assertion for `PinSections` was added by commit `b417a8bfe`.
- The test constructed:

```go
web := NewWebServer(hubcore.WebConfig{HubAddr: ..., Roster: r, Past: hubcore.NewPastIndex(""), Favorite: favStore})
```

- There was **no** `PinSections` store in the test config, so `handleAPITree` had nowhere to migrate/serve named pin section data.
- The product behavior was therefore correct; the test harness was incomplete for its new assertion.

**Conclusion**
- This was a broken test setup, not a second production regression. The minimal fix was to provide `PinSections: hubcore.NewPinSectionStore(...)` in the test.

## Changes made

### 1) Restore full favorite presentation for tree row rendering
**File:** `cmd/serf-hub/web_api_tree.go`

Changed:

```diff
- favs := projectFavoritePresentation(revalidation.Presentation)
+ favs := revalidation.Presentation
```

Why:
- Session rows (`apiTreeNodeTier`) need session favorite decisions.
- Project rows still work because they read project keys from the same full map.

### 2) Fix stale generated protocol file
**File:** `cmd/serf-hub/frontend/src/protocol/types.gen.ts`

Changed `JobActivityJob` field order so the emitted TypeScript matches `appwire/types.go` declaration order:

```diff
  ownerRef: string;
+ transcriptRef?: string;
  type: string;
  status: string;
  outcome?: string;
- transcriptRef?: string;
```

Why:
- This matches `internal/appwirets` emission semantics and satisfies `TestGeneratedFileCurrent`.

### 3) Fix orphan-live test harness for named pin assertion
**File:** `cmd/serf-hub/web_api_tree_test.go`

Added a real pin-section store before asserting `PinSections` contents:

```diff
+ pins := hubcore.NewPinSectionStore(filepath.Join(root, "index.db"))
...
- web := NewWebServer(hubcore.WebConfig{..., Favorite: favStore})
+ web := NewWebServer(hubcore.WebConfig{..., Favorite: favStore, PinSections: pins})
```

Why:
- The test now provisions the feature it asserts against.

## Commands run and outputs

### Git state and history
```bash
git status --short
git branch --show-current
git log --oneline --decorate -10
```

Key output:
- Branch: `delegate-send-always-wakes`
- Relevant recent commits included:
  - `7a9213647 fix: finalize delegate send always-wakes review`
  - `21530dbe1 feat: remove on_idle from delegate_send schema`
  - `38c817949 fix: make delegate sends wake idle delegates`

### Base comparison
```bash
git merge-base HEAD main
```
Output:
```text
26d8abc0335470da690f1bbd26256733347cc2a0
```

### Tree regression comparison
```bash
git show 26d8abc0335470da690f1bbd26256733347cc2a0:cmd/serf-hub/web_api_tree.go | sed -n '130,170p;1118,1182p'
git blame -L 150,156 -- cmd/serf-hub/web_api_tree.go
git blame -L 340,347 -- cmd/serf-hub/web_api_tree.go
git blame -L 1173,1179 -- cmd/serf-hub/web_api_tree.go
```

Key findings:
- Base passed full `ClassifyFavoriteDecisions(...).Presentation` to row rendering.
- Current branch narrowed that to `projectFavoritePresentation(...)` in code introduced by `b417a8bfe`.

### Generation workflow inspection
```bash
rg -n "go:generate|appwirets|types.gen.ts|TestGeneratedFileCurrent|EmitCatalog\(" appwire internal/appwirets cmd/serf-hub/frontend
```

Key findings:
- `appwire/doc.go` contains:

```go
//go:generate go run primeradiant.com/serf/internal/appwirets -out ../cmd/serf-hub/frontend/src/protocol/types.gen.ts
```

- `internal/appwirets/emit_test.go` contains the drift guard:

```go
func TestGeneratedFileCurrent(t *testing.T) {
    want := EmitCatalog()
    got, err := os.ReadFile("../../cmd/serf-hub/frontend/src/protocol/types.gen.ts")
    if err != nil || string(got) != want {
        t.Fatal("types.gen.ts stale: run `make generate`")
    }
}
```

### History proving stale generated file
```bash
git show 417a6238d -- appwire internal/appwirets cmd/serf-hub/frontend/src/protocol/types.gen.ts
git show 8717f8495 -- appwire internal/appwirets cmd/serf-hub/frontend/src/protocol/types.gen.ts
git log -S 'TranscriptRef  string `json:"transcriptRef,omitempty"`' -- appwire/types.go
```

Key findings:
- `8717f8495` introduced generated `JobActivity*` types.
- `bace97fed` introduced `TranscriptRef` in `appwire/types.go`.
- `417a6238d` updated `types.gen.ts`, but the field was placed in a position inconsistent with generator order.

### Verification
To avoid earlier stuck `go test` processes in this harness, I reran the targeted tests with isolated Go cache/temp dirs.

```bash
set -euo pipefail
export GOCACHE="$(mktemp -d)"
export GOTMPDIR="$(mktemp -d)"
go test ./cmd/serf-hub -run '^(TestWeb_APITreeLiveRowsCarryTierFavoriteRename|TestWeb_APITreeOrphanLiveRowsCarryTierFavoriteRename)$' -count=1 -v
```
Output:
```text
=== RUN   TestWeb_APITreeLiveRowsCarryTierFavoriteRename
--- PASS: TestWeb_APITreeLiveRowsCarryTierFavoriteRename (0.05s)
=== RUN   TestWeb_APITreeOrphanLiveRowsCarryTierFavoriteRename
--- PASS: TestWeb_APITreeOrphanLiveRowsCarryTierFavoriteRename (0.03s)
PASS
ok  	primeradiant.com/serf/cmd/serf-hub	0.558s
```

```bash
set -euo pipefail
export GOCACHE="$(mktemp -d)"
export GOTMPDIR="$(mktemp -d)"
go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1 -v
```
Output:
```text
=== RUN   TestGeneratedFileCurrent
--- PASS: TestGeneratedFileCurrent (0.00s)
PASS
ok  	primeradiant.com/serf/internal/appwirets	0.168s
```

## Changed files

- `cmd/serf-hub/web_api_tree.go`
- `cmd/serf-hub/web_api_tree_test.go`
- `cmd/serf-hub/frontend/src/protocol/types.gen.ts`

## Notes / concerns

- I attempted several direct `go test` and `go run ./internal/appwirets ...` invocations earlier in this harness; some were promoted to background after the session command timeout with no captured output. I stopped those processes, reset the test state, and completed verification with isolated `GOCACHE`/`GOTMPDIR`, which produced deterministic results.
- The final `types.gen.ts` change is the regeneration-equivalent correction proven by the generator’s field-order rules and by `TestGeneratedFileCurrent` passing. I did **not** change `internal/appwirets` logic because the generator was not the root cause.
