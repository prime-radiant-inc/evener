# Provider Image Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Continue a rejected image turn through bounded, color-managed same-model recovery without changing the original input.

**Architecture:** A pure-Go image helper converts supported matrix/TRC RGB ICC profiles to sRGB and applies orientation. The Chat Completions adapter repairs private final-wire message copies after a definite HTTP rejection, then retries through existing transport accounting. Successful unmanaged recovery reaches both model context and session diagnostics.

**Tech Stack:** Go standard library, ICC matrix/TRC subset, independent LittleCMS reference fixtures, scripted HTTP provider tests.

**Spec:** `docs/superpowers/specs/2026-10-19-provider-image-recovery-design.md`

## Global Constraints

- Original attachments, caller objects, history, registry overlays and successful requests remain unchanged.
- `CGO_ENABLED=0`; no runtime Python, ImageMagick or system-library prerequisite.
- Same endpoint and model; at most two repair resends. No replay after stream exposure.
- JPEG only; no new remote fetch, filesystem read, resizing, dropped image or multipicture flattening.
- MPF/multipicture input remains unchanged in both repair paths.
- Repair-only expanded-PNG 413 with no distinct smaller fallback returns the original image rejection.
- Tests assert behavior and structured values, never human-readable error or warning fragments.
- No private attachment fixtures in Git. Default tests require no provider credentials or network.

## Review Focus

- Provider options, transport constants and request preparers can replace messages; repair the final shape and retain all unrelated fields.
- ICC tables can overlap, be duplicated or contradict RGB matrix/TRC assumptions; unsupported profiles use disclosed fallback.
- Metadata removal can invalidate multipicture offsets; preserve those JPEGs byte-for-byte.
- Repaired PNG expansion can trigger context compaction unless its 413 is kept out of operational error routing.
- A successful repaired stream can later emit tools; exactly-once tool execution and original-image history must survive.

## File structure and interfaces

- `llm/internal/imagerepair/{jpeg.go,icc.go,orientation.go}`: bounded parsing, color conversion, metadata-only fallback. Tests and synthetic/reference data beside these files.
- `llm/providers/chatcompletions/image_recovery.go`: final-wire traversal, immutable copies, bounded stage state and warning construction.
- `llm/providers/chatcompletions/{complete.go,stream.go}`: use the shared recovery owner and carry successful warnings.
- `llm/{errors.go,classify_http.go}`: structured recognition and removal of unrelated field advice.
- `agent/session_model_call.go`: surface the successful unmanaged warning through existing warning events.
- `agent/provider_image_recovery_test.go`: real session, transport, tool and persistence journey.
- `docs/{llm-providers.md,product/subsystems.md}`: verified ownership, behavior and limits.

Image helper public interface:

```go
type Mode uint8
const (
    PreferColorManaged Mode = iota
    ProfileFree
)
type Method uint8
const (
    Unchanged Method = iota
    ColorManaged
    ProfileRemoved
)
type Result struct {
    Data []byte
    MediaType string
    Method Method
}
func RepairJPEG(ctx context.Context, data []byte, mode Mode) (Result, error)
```

Unsupported or malformed input returns unchanged bytes and `Unchanged`, not a new terminal error. Context cancellation is an error. `PreferColorManaged` falls back to `ProfileRemoved` only when safe conversion is unavailable. `ProfileFree` removes ICC only. Both refuse MPF/multipicture mutation. Limits: 32 MiB encoded JPEG, 16 million decoded pixels, 1 MiB aggregate ICC, 64 MiB encoded PNG. Bounds limit recovery, never reject the original input. Conversion evaluates validated matrix/TRC profiles in their D50 PCS, converts to sRGB, clips outside gamut, and never selects a transform by profile name. Unsupported LUT-based, non-RGB, contradictory or invalid profiles remain eligible for safe ICC-only fallback.

## Task 1: Portable color-managed JPEG repair

**Files:** Create the `llm/internal/imagerepair/` files and tests above, plus fixture provenance/generation instructions under its `testdata/`.

**Interfaces:** Produce `RepairJPEG`, `Mode`, `Method`, `Result` exactly as defined above. No other package changes or dependencies.

- [ ] Write tests for clean RGB JPEG to PNG, Display P3 and sRGB matrix/TRC fixtures versus independent LittleCMS samples, neutral and saturated colors, orientations 1–8, unchanged source bytes, unsupported/malformed profiles, ICC chunk ordering and budget boundaries. Include MPF, concatenated images, progressive JPEG, unrelated APP2, invalid marker lengths, cancellation and no-ICC profile-free no-op. PNG output is opaque because input JPEG has no alpha.
- [ ] Run `CGO_ENABLED=0 go test ./llm/internal/imagerepair -count=1` and record the expected missing-repair failures before implementing.
- [ ] Implement the interface with bounds-checked marker/ICC/TIFF parsing, explicit supported profile subset, context checks throughout bounded image work and clean PNG encoding. Add parser fuzz seeds.
- [ ] Run `CGO_ENABLED=0 go test ./llm/internal/imagerepair -count=1` and `go test -race ./llm/internal/imagerepair -count=1`. Confirm fixtures were generated independently, not from the conversion implementation.
- [ ] Stage only the new helper tree, inspect the staged diff and commit.

## Task 2: Bounded provider recovery and truthful classification

**Files:** Create `llm/providers/chatcompletions/image_recovery.go` and `image_recovery_test.go`; modify `complete.go`, `stream.go`, `llm/errors.go`, `llm/classify_http.go`; add `llm/image_normalization_test.go`.

**Interfaces:** Consume Task 1. Produce `llm.IsImageNormalizationError(error) bool`, `llm.WarningCodeImageColorUnmanaged = "image_color_unmanaged"`, and successful `llm.Response.Warnings` entries for unmanaged image repair. Keep existing public protocol signatures.

- [ ] Add a real HTTP test for both `Complete` and `Stream`: original JPEG gets 400, second request contains a decodable same-size PNG and succeeds, exactly two HTTP exchanges, same model, no mutation. Add classifier cases with provider fixture text as input: mixed-case 400/422 recognized, unrelated errors excluded, unrelated field hint absent, actual rejected field guidance retained.
- [ ] Run `go test ./llm ./llm/providers/chatcompletions -run 'TestImageNormalization|TestImageRecovery' -count=1`; observe missing recovery and incorrect hint behavior.
- [ ] Implement a shared attempt owner for both entry points. Build each attempt fresh and chain repair after `reconcilePreparedOutput` in `FinalizeBody`. Copy only the messages subtree with JSON `UseNumber` to accept typed maps, raw JSON and ordinary slices without mutating overlays or losing numeric precision. Traverse only Chat Completions image parts. Keep all unsupported images and unrelated fields. Insert fixed model-facing caveats beside unmanaged images.
- [ ] Add red tests for managed rejection then ICC fallback; malformed ICC direct fallback; no identical resend; PNG 413 then smaller fallback; no-ICC PNG 413 returns original 400; mixed/repeated images; no-change input; remote URLs; unrelated 400; cancellation; streaming failure after exposure; all actual attempts recorded. Cover provider overlays, transport constants and preparer overrides with caller-object preservation. Bound aggregate decode work to 32 million pixels per repair attempt, leaving remaining images eligible for safe metadata-only repair.
- [ ] Pass those tests. Record original error separately from last repair error. Expose stream warnings only in the final successful response and never replay the stream decoder.
- [ ] Run `go test ./llm ./llm/providers/chatcompletions ./llm/providers/responses ./llm/providers/anthropic -count=1`, then stage exact touched paths, inspect and commit.

## Task 3: Session recovery, preservation and documentation

**Files:** Modify `agent/session_model_call.go`, create `agent/provider_image_recovery_test.go`, update `docs/llm-providers.md` and `docs/product/subsystems.md`.

**Interfaces:** Consume Task 2 warning code and real protocol behavior. Emit existing `events.WarningData` with `Code: llm.WarningCodeImageColorUnmanaged` after successful model settlement. No new client state or retry owner.

- [ ] Add a scripted HTTP provider journey using real session/model wiring and real tool dispatch: input image is rejected, repaired request produces one tool call, tool executes once, session finishes, transcript reload preserves original bytes, subsequent call still starts with the original image. Include unmanaged success warning and the no-ICC/PNG413 configured-fallback case, asserting no forced compaction.
- [ ] Run `go test ./agent -run 'TestProviderImageRecovery' -count=1`; observe the missing diagnostic behavior before adding its bridge.
- [ ] Surface the one warning code through the existing session diagnostic event at successful settlement. Update the owning docs with actual supported profiles, budgets, retry limits and preservation boundaries.
- [ ] Run `go test ./agent -run 'TestProviderImageRecovery' -count=1`; run touched package lint and targeted regression checks. Full suite runs in CI.
- [ ] Run the copied private failing JPEG through the built client against the same provider/model, explicitly live. Inspect original-versus-repair attempt data, successful comprehension and unchanged source hash. Do not commit private image data or credentials.
- [ ] Stage exact task files, inspect and commit.

## Task 4: Required delivery sequence

- [ ] Review immutable implementation diff and evidence with two independent PAR reviewers; resolve legitimate findings red-first.
- [ ] Run `simplify-code:simplify-code`; implement warranted quality changes and rerun affected tests.
- [ ] Run the requested RoboRev branch review and resolve findings against the reviewed head.
- [ ] Run `primeradiant-ops:shepherd-pr`: one push, one matching CI/review wait per round; settle own per-commit findings and current-head combined review.
- [ ] Merge only under repository authorization, verify GitHub merged state and intended content, remove owned disposable worktree/scratch, and report PR, checks, live result and remaining limits. No hub deployment.
