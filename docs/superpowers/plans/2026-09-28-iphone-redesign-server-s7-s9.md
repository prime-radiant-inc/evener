# iPhone redesign, Phase 7: remote documents and document revisions (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Every task carries its code, so every task's implementer is Sonnet; the reviewer checks it against this plan and the code as it is on main.

**Goal:** The Reader can say how many paragraphs changed since you last read a document from what the hub says about the file itself (S9), and plans and documents in a session on another host open in the Reader and on the web instead of showing "Open it on the host" (S7).

**Architecture:**
- **S9 (PR 34).** Every raw `/doc/file` read names the version it served: a strong `ETag` holding the sha256 of the whole file (not only the 512 KiB head it serves), and `X-Doc-Modified-At` with the file's modification time in Unix milliseconds. The route answers `If-None-Match` with 304 and sends `Cache-Control: private, no-cache`, so no cache shows an old version. The shared package's `readDocFile` puts the two on `DocFileContent` as `revision` and `modifiedAt`.
- **S7 (PR 35).** Images in remote sessions already render: #2462 (multi-host component 05) proxies `/doc/image` and `/s/<id>/images/<sha>` through `evener/session/image`. Documents are the missing half. Every hub learns `evener/session/document`, which reads a file from its own session folder by the local route's exact rule and answers with the head, the true size, the revision and the time. The controller's `/doc/file` sends a host-qualified session id (`<host>:<session>`) through the owning host's attached channel and answers with the local route's own headers. The shared package and the web learn one new failure kind, `host-unsupported` (501), for a host whose hub predates the method.

**Tech Stack:** Go 1.27 workspace (root module), AppWire over WebSocket (`ProtocolVersion` `"evener-appwire-v6"`), TypeScript 6 in `appwire-client/typescript` (tested with vitest from `cmd/evener-hub/frontend`), the web's doc pane (`cmd/evener-hub/frontend/src/panes/doc`).

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`: 10.2 (the Reader: "Plan · updated 3m ago", "3 changes since you read it yesterday", the 512 KB note), 8.2 (the document chip's "changed since you last read"), 17 and 18 (S7, S9). The server plan `docs/superpowers/plans/2026-09-26-iphone-redesign-server-additions.md` holds the design-level S7 and S9 sections this plan replaces and its Global Constraints. The phone side is the phase 4 plan's rulings 13 (changes since you last read, S9's fallback), 17 ("updated 3m ago" from the opener) and 18 (files on another host get one sentence, S7's fallback). Written and dry-run against main at `947c50235` (see Self-review).

## What was measured

Everything below was read on main at `947c50235`.

**How a document is read today.**
- `/doc/file` (`cmd/evener-hub/doc_serve.go:40`) serves only `?format=raw`. It resolves the session's working directory with `sessionCWD` (`:149`), which refuses any id naming another source (`:150`, through `isLocalRouteID`, `cmd/evener-hub/web.go:363`), then confines the path with `fspaths.ResolveInRoot` (`:58`).
- `ResolveInRoot` (`cmd/evener-hub/internal/fspaths/paths.go:81`) checks containment twice: the cleaned join must sit under the symlink-resolved root (`:99`), and so must the symlink-resolved target (`:109`). An absolute path is accepted only when it already lies inside the root (`:93`). An escape is `ErrPathEscapesRoot`, which the route answers 403 (`doc_serve.go:62`); anything else that does not resolve is 404.
- The check and the read are separate: `ResolveInRoot` checks the path, then `readDocFile` (`doc_serve.go:176`) stats and opens it again by name (`:177`, `:184`). A symlink swapped in between the check and the open leads the open out of the folder. `/doc/image` and `evener/session/image`'s path branch have the same gap (filed as #2918). The stat comes first so a FIFO is never opened, then `readDocFile` reads the head with one `f.Read` into a 512 KiB buffer (`:189-194`). An empty file returns `(0, io.EOF)` from that read and is answered 404, as if it were missing.
- `writeDocFileRaw` (`:225`) sends `text/plain` or `application/octet-stream` by a NUL-byte sniff, and `X-Doc-Truncated` / `X-Doc-Total-Size` past the cap. The total comes from a second stat, `docRawTotalSize` (`:244`), so it can describe a different version than the bytes.

**What identity a document read carries today: none.**
- `/doc/file` sends no `ETag`, no `Last-Modified` and no `Cache-Control`, and ignores `If-None-Match` (`doc_serve.go:225-236`).
- `/doc/image` sends a sha256 `ETag` of the bytes it served and `Cache-Control: private, max-age=60`, and never answers 304 (`:132-135`).
- `DocFileContent` has no revision or time (`appwire-client/typescript/docContent.ts:16`), and `readDocFile` returns `{ text, binary, mediaType, truncated, sizeBytes, totalBytes }` (`:128`).
- The phone's Reader keeps the version you last read as block hashes (`LastRead`, `mobile-native/src/reader/documentMemory.ts:44`) and counts changes with `changedBlocks` (`mobile-native/src/reader/documentChanges.ts:11`), the phase 4 plan's S9 fallback. Its "updated 3m ago" comes from the opener's `updatedAt` route parameter (`mobile-native/src/reader/ReaderScreen.tsx:265`); without one the caption shows only the kind (phase 4 ruling 17).
- The Reader re-reads the whole document on mount, when the connection comes back and whenever the app returns to the front (`mobile-native/src/reader/useDocument.ts:61`).

**Remote sessions: images are done, documents are not.**
- #2462 added `evener/session/image` (`appwire/types.go:253`, catalog `appwire/protocol.go:276`), served by every hub from its own state (`sessionImageFromHub`, `cmd/evener-hub/image_serve.go:234`, registered at `cmd/evener-hub/app_rpc.go:1291`). The controller's two image routes branch on a host-qualified id (`hostQualifiedImageRef`, `cmd/evener-hub/image_proxy.go:65`) and proxy through `RemoteHubSource.FetchSessionImage` (`cmd/evener-hub/internal/appsource/remote_hub_source.go:596`), which uses the host's attached channel only and never dials. The proxy re-checks the host's answer before serving it (`proxyableSessionImage`, `image_proxy.go:46`) and never falls back to a local read.
- The remote hub source rewrites every image URL a host stamped into the controller's host-qualified form (`rewriteRemoteImageURL`, `cmd/evener-hub/internal/appsource/remote_hub_images.go:47`). So the phone's transcript images and the Reader's image view (`documentSource.ts` sends an image path straight to `/doc/image`) already work for remote sessions. S7's image half needs nothing.
- `/doc/file` has no remote branch: a host-qualified id fails `sessionCWD` and is answered 404. The phone never asks: `loadDocument` returns `{kind: "elsewhere"}` for any session whose ref names a host other than `local` (`mobile-native/src/reader/documentSource.ts:54`), shown as "This document is on <host>. Open it on the host to read it." (`:82`). The web asks and shows "File not available" (`cmd/evener-hub/frontend/src/panes/doc/DocPane.tsx:35`). The server plan's note that the web has a local-only check in `fileOpenBeside.tsx` is stale: that file has none today.
- `evener/session/image` is denied on the host admin proxy (`cmd/evener-hub/app_host_admin_test.go:534`, the comment at `:399`): it is the controller's own route call, and a peer must not drive it by forwarding. The server plan's sketch put both methods on the allow-list; the new method follows the image method instead.
- `evener/session/image` refuses an absolute path (`image_serve.go:289`), while `/doc/file` accepts one inside the folder. A document proxy must answer as the local route does, so the new method follows `/doc/file`.

**Sizes, measured on this machine.**
- Documents: 66,387 markdown files under `~/git` (node_modules, .git and worktrees left out): median 3.8 KB, p99 79 KB, p99.9 234 KB, largest 3.2 MB; 16 are over 512 KiB. The largest plan in this repository is 463 KB. So the existing 512 KiB cap (`docFileMaxBytes`, `doc_serve.go:23`) holds for a proxied document too, and it costs about 700 KB of base64 in one AppWire frame.
- Images: 30,275 png/jpeg/gif/webp files under `~/git`: median 72 KB, p99 1.4 MB, 85 (0.28%) over 8 MiB. The existing 8 MiB bound (`outputImageMaxBytes`, `cmd/evener-hub/output_images.go:27`), about 11 MB base64, stays.
- Frames: both transports cap one AppWire message at 128 MiB (`appWireWebSocketReadLimit`, `appwire/ws_transport.go:35`; the ssh stdio transport reuses it, `appwire/stream_transport.go:27`). Neither cap comes near it.
- Hashing: sha256 over a 16 MiB file read from the page cache took 7.4 ms (2.3 GB/s) on this M4 Max (`go test -bench`, 20 runs). Hashing the whole file is cheap below 16 MiB, which covers every markdown file measured with five times headroom.

**Errors on the proxy path.** `statusForWireError` (`cmd/evener-hub/web_api.go:43`) maps MethodNotFound to 404, which on `/doc/file` would read as "this file isn't in the session's folder any more". A host on an older build answers MethodNotFound until sshconn redeploys it (a host whose build differs from the controller's is redeployed before attaching, `cmd/evener-hub/internal/sshconn/doc.go:17-19`), so the proxy maps it to its own status.

## Global Constraints

- **Wire.** Every change is additive and stays on `ProtocolVersion = "evener-appwire-v6"` (`appwire/types.go:30`).
  - `evener/session/document` is a new method. A hub without it answers MethodNotFound, which the controller turns into 501.
  - `SessionDocumentResponse.revision` and `.modifiedAt` are `omitempty`; `revision` is absent for a file over 16 MiB.
  - `pathOutsideSession` is a new `evenerErrorInfo` value on the existing `CodeInvalidParams`; a client that does not know it sees an invalid-params error.
  - HTTP: `ETag`, `X-Doc-Modified-At` and `Cache-Control` are new response headers, and 304 is answered only to a request that sends `If-None-Match`. An older client reads none of them.
  - `DocFileContent.revision` and `.modifiedAt` are optional and absent when the hub sends no header. `DocFileErrorKind` gains `"host-unsupported"`; the one exhaustive consumer, the web's `ERROR_COPY`, gets its row in the same task.
- **Never add a `FeatureSet` key.** The TS `initialize` decoder refuses unknown feature keys.
- **Casing.** `appwire` JSON is camelCase (`totalSize`, `modifiedAt`, `pathOutsideSession`). The tagliatelle lint enforces it (`.golangci.yml`).
- **Confinement.** The remote read is confined by the same code as the local one: the host resolves the path with `sessionCWD` and `fspaths.ResolveInRoot`, inside the session's folder on its own disk. The controller never resolves a remote path and never reads its own disk for a host-qualified id. See ruling 3.
- **Generated files.** After the `appwire` change in Task 35.1: `make generate`, then `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`. Commit `appwire-client/typescript/types.gen.ts` and `docs/appwire-protocol.md`.
- **A new hub method** gets a catalog row (`appwire/protocol.go`), a decision in `TestHostAdminAllowListMatchesCatalog` (`cmd/evener-hub/app_host_admin_test.go`), and an entry in `TestHubRPCRegistersExpectedHandlerSet` (`cmd/evener-hub/app_rpc_test.go`). It is hub-scoped, so no daemon retirement row.
- **The TUI rule.** No PR changes what `internal/appprojector` or `internal/apptranscript` emits, and no PR adds a notification, so no TUI case is needed.
- **Go floors** (root module only):
  - `go vet ./cmd/evener-hub/... ./appwire/...`, the same with `-tags evenerfuzz`, and `GOOS=windows go vet -tags evenerfuzz` on both;
  - format with `$(go env GOROOT)/bin/gofmt`, never the one on PATH;
  - the pinned `golangci-lint` (2.13.1, `.tool-versions`) on `./cmd/evener-hub/`, `./appwire/` and `./cmd/evener-hub/internal/appsource/`. Its `modernize` check wants `strings.SplitSeq` and `maps.Copy` where the code below uses them.
- **Coverage-seed tests.** `readDocFile`, `looksBinaryBytes` and `handleDocFile` are driven by the fuzz seeds `FuzzSmallFaultsPass5` (`cmd/evener-hub/cov_small_faults_pass5_fuzz_test.go`) and `FuzzCovThreadreadImagesSeed100` (`cmd/evener-hub/cov_threadread_images_fuzz_test.go`), which `make test` skips. Run them with `go test ./cmd/evener-hub -run '^(FuzzSmallFaultsPass5|FuzzCovThreadreadImagesSeed100)$' -count=1` after touching `doc_serve.go`.
- **TypeScript floor.** From `cmd/evener-hub/frontend`: `npm run typecheck`, `npx vitest run ../../../appwire-client/typescript/docContent.test.ts src/panes/doc/`, and `npx biome check --write <touched files>` on touched files under `src/` and in `appwire-client/typescript`. PR 35 also runs `npm run check` in `mobile-native` (the phone imports `DocFileErrorKind`'s values). Never run Biome from the repo root or in `mobile-native`, and never `npm ci` through a symlinked `node_modules`.
- **Targeted tests only.** Run each task's tests and the gates it names; CI runs the full matrix. Some `cmd/evener-hub` tests fail on macOS only (#2497); CI (Linux) is the judge.
- **Deterministic tests.** No network, no sleeps, no wall clock: files get fixed modification times with `os.Chtimes`, the host in the proxy tests is either a real hub served by `httptest` or a scripted AppWire peer (`newScriptedRemoteHub`), and the large-file case uses a sparse file. Every new test is shown failing before its code lands.
- **Line numbers** are on main at `947c50235`. A merge from another lane moves them, so every step also names its anchor: find it by the name.
- **Size,** measured on the dry run (production lines, tests and generated output excluded): PR 34 about 210 (Go 146 added and 38 removed, TypeScript 28), PR 35 about 260. Landing follows the handoff: a regular PR, CI green on the merged head, RoboRev's comment read, /simplify run and its fixes pushed, then an admin squash merge with `--match-head-commit <full sha>`.

## Rulings

Decisions the spec and the server plan leave open, with the reason for each.

**S9, document revision identity**

1. **The revision is the sha256 of the whole file's bytes.** The server plan sketched a weak tag from size, time and the head's hash. A content hash is simpler and stronger: touching a file without changing it keeps its revision, and an edit past the 512 KiB head changes it. It costs one full read, measured at 7.4 ms for 16 MiB.
2. **Files over 16 MiB (`docRevisionMaxBytes`) carry no revision.** A read hashes at most 16 MiB and one byte (about 7 ms), so a huge log costs no more than that on every foreground; past the limit the phone falls back to comparing what it was shown, as today. The limit is 5 times the largest markdown file measured.
3. **Size and revision come from one pass.** The read hashes exactly the bytes it counted, and those bytes, never the stat's size, decide the revision, so the total and the revision describe one version even while the file is being written. A file that grows past 16 MiB after the stat gets no revision and a size of at least what the read found; one that shrinks below it is hashed and reports the size read (both found by review on the plan PR). This replaces `docRawTotalSize`'s second stat.
4. **The time rides as `X-Doc-Modified-At` in Unix milliseconds,** and only when it is after the epoch (`docModifiedMillis`), so the server and the shared package agree that zero or less means no time. It is not `Last-Modified`: `Last-Modified` has one-second precision and an HTTP-date the phone would have to parse by hand (whether React Native's Hermes parses that date format was not checked, so the plan avoids it), and it would invite heuristic caching.
5. **The shared package trusts only the hub's own tag shape** (RoboRev on the plan PR): `etagRevision` accepts a strong, quoted, 64-character lowercase hex tag and nothing else, so a weak or rewritten tag from something between the phone and the hub falls back to comparing what was shown. **`Cache-Control: private, no-cache`, and `If-None-Match` answered by the rules of RFC 9110 13.1.2** (weak comparison, and `*` matches any current version, including one too large to have a revision). Every cache must revalidate before reuse, so a changed file is never shown from a cache; with the `ETag`, revalidation costs a 304 and no body. Browsers send `If-None-Match` for such a response on their own (RFC 9111). Whether iOS's `NSURLSession` under React Native's `fetch` does the same was not measured, and nothing here relies on it: the phone reads `revision` from the response either way.
6. **An empty file is served empty, not 404.** The single `f.Read` treated it as missing. The rewrite reads with `io.ReadFull`, so the phone no longer says an empty file "isn't in this session's folder any more".

**S7, remote documents**

7. **Images need nothing.** They already proxy (What was measured). S7 is documents only.
8. **The method is `evener/session/document`,** beside `evener/session/image`: params `{sessionId, path}`, response `{data, totalSize, revision?, modifiedAt?}`. `data` is bytes (base64 in JSON), because the head may end mid-rune or be binary. The controller re-derives text or binary from the bytes, as the image proxy re-derives the media type.
9. **Confinement: the host resolves, by the local rule.** The host runs `sessionCWD` (which refuses an id naming another source, so a request cannot be chained to a third hub) and `fspaths.ResolveInRoot` (lexical and symlink containment) against its own session folder. An escape is a new typed refusal, `pathOutsideSession`, which the controller answers 403 as the local route does. An absolute path inside the folder is accepted, as on `/doc/file`. The controller forwards the path untouched and never resolves it; it holds no path of the host's disk to resolve against. The method opens nothing the HTTP route does not: any client of a hub that could call it could already fetch the same file from that hub's `/doc/file` with the same credentials. **The read is confined too, not only the check** (RoboRev on the plan PR): `readDocFile` opens the checked path through an `os.Root` at the session folder (`openDocInRoot`), which refuses a path that resolves outside it at the moment of the open, so a symlink swapped in after `ResolveInRoot` cannot lead the read out. The open is non-blocking on Unix (`docOpenNonblock`), and the stat is of the open descriptor, so a FIFO swapped in is refused without waiting for a writer. PR 34 lands this, since it rewrites `readDocFile`, and the local `/doc/file` gets it too.
10. **The controller re-checks the host's answer** (`proxyableSessionDocument`): at most 512 KiB, a total at least that long, a full head when truncated, a revision exactly when the total is 16 MiB or less (the host hashes every such file, so a missing one would silently turn S9 off), and a revision that is a sha256 and, when the bytes are the whole file, their own sha256. A violation is 502, never served. The check reuses `imageSha` and `imageShaRegexp`, the package's sha256-hex helpers; renaming them would touch the image code for no behavior change, so it is left out.
11. **Status mapping** (`sessionDocumentProxyStatus`): `pathOutsideSession` 403, `resourceNotFound` 404, invalid params 400, MethodNotFound 501 (the host predates S7), anything else 503 (host detached, unknown or unreachable, or the host's own internal error or conflict; the mapping names every code rather than falling through to `statusForWireError`, which would answer 500 or 409). The shared package reads 501 as `host-unsupported`, the phone's cue to keep "Open it on the host".
12. **The method is denied on the host admin proxy,** like `evener/session/image`.
13. **One helper finds the owning source.** `sessionImageFetcher` becomes the generic `owningSourceAs[T]`, `hostQualifiedImageRef` becomes `hostQualifiedRouteRef` and `remoteSessionImageBudget` becomes `remoteSessionFileBudget`, since images and documents now share them. The rename is mechanical: three call sites.
14. **A host-qualified request is checked for `format=raw` before the host is asked,** so the controller never forwards a request it would refuse.

## Questions for Jesse

Each has a recommendation, and the coordinator has directed that the plan be built to the recommendations (2026-09-28): the Phone lane handoff below carries each one. None blocks the server work; they are product questions for the phone lane's switch-over, which Jesse can still overturn there.

1. **What does the Reader say when the revision changed but no paragraph you can see did?** It happens when the edit is past the 512 KB the Reader shows, or changes only markup the block hashes ignore. **Recommendation:** say nothing. The caption counts changes you can step to; "changed" with nothing to show would send you hunting. The alternative is "Changed past the part shown" when the document is truncated.
2. **Which time does "updated 3m ago" show?** The opener knows when the session last wrote the file; S9 gives the file's own modification time, which also moves when you or another tool edit it. **Recommendation:** the file's own time whenever the hub sends it, since the caption describes the document you are reading. The opener's time stays the fallback for an older hub.
3. **What does a document on a host that is offline say?** The proxy answers 503, which the phone shows as "<file> couldn't be loaded right now" and re-reads when the connection comes back. **Recommendation:** keep that sentence. Naming the host ("studio is offline") would be friendlier, but the Board's host notice already says so, and the Reader would need the host list to say it.

## Review Focus

1. **A remote read that escapes the session's folder.**
   - A `..` path, an absolute path outside the folder, and a symlink inside it that leads out must be refused on the host, and answered 403 by the controller, never served.
   - A session id naming a third hub must not be followed.
   - A symlink swapped in after the check must not lead the open out, and a FIFO must not block the read.
   - Pinned by `TestReadDocFile_OpenStaysInsideTheRootAfterASwap` and `TestReadDocFile_RefusesAFIFOWithoutBlocking` (Task 34.1), `TestHubSessionDocumentStaysInsideTheSessionFolder` (Task 35.1) and the 403 cases of `TestDocFileRouteReadsARemoteSessionThroughItsHost` (Task 35.2), which run a real host hub over a real AppWire channel.
2. **A remote id read from the controller's own disk.**
   - A host-qualified id must never reach `sessionCWD` on the controller, even when the host is detached, unknown or refuses.
   - Pinned by `TestDocFile_Raw_UnknownHostSession503`, `TestDocFileRouteRefusesBeforeAskingTheHost` and `TestDocFileRouteMapsHostRefusals` (Task 35.2).
3. **An old version shown from a cache.**
   - The revision headers must not let a browser or the phone show a document that changed since.
   - Pinned by `TestDocFile_Raw_NamesTheRevisionItServed` (`private, no-cache`) and `TestDocFile_Raw_IfNoneMatchRevalidates` (200 with the new bytes after an edit) (Task 34.1).
4. **A change the revision misses, or a size that lies.**
   - An edit past the 512 KiB head must change the revision. A file at exactly the 16 MiB limit is still hashed, one that grows past it after the stat reports no revision and a size no smaller than what was read, and one that shrinks below it is hashed.
   - Pinned by `TestDocFile_Raw_RevisionFollowsTheWholeFile`, `TestDocFile_Raw_RevisionAtTheHashLimit`, `TestReadDocFile_GrowingPastTheHashLimitReportsWhatWasRead` and `TestReadDocFile_ShrinkingBelowTheHashLimitIsHashed` (Task 34.1).
5. **An old host that reads as a missing file.**
   - A host without the method must not make the phone say the file is gone.
   - Pinned by the 501 case of `TestDocFileRouteMapsHostRefusals` (Task 35.2) and the `host-unsupported` tests in `docContent.test.ts` and `DocPane.test.tsx` (Task 35.3).

---

## PRs and lanes

| PR | Item | Tasks | Production lines (dry run) | Depends on |
|---|---|---|---|---|
| 34 | S9: document revision identity | 34.1-34.2 | about 210 | none |
| 35 | S7: documents in remote sessions | 35.1-35.3 | about 260 | PR 34 |

- **Merge order.** One lane: PR 34, then PR 35 branched from main once PR 34 has merged. PR 35's host method returns what PR 34's `readDocFile` reads, and its proxy writes through PR 34's `writeDocFileRaw`.
- **Where this meets other lanes.** `appwire/types.go`, `appwire/protocol.go`, `appwire/errors.go` and the generated files (PR 35). When `types.gen.ts` or `docs/appwire-protocol.md` conflicts, take either side and run `make generate` again.
- **The server plan's PR map changes with this plan:** S7 and S9 were PRs 26-27 and 28 there; they are PRs 35 and 34 here. S7 is one PR, since its image half had already landed.

## Phone lane handoff

This lane changes `mobile-native/` only through the shared package. Once each PR is on the hub the phone talks to, the phone switches off its fallback as follows.

**S9 (after PR 34).** Phase 4's Reader (rulings 13 and 17).
- `LoadedDocument`'s markdown and code kinds carry `revision` and `modifiedAt` from `DocFileContent` when present.
- `LastRead` stores the `revision` it was read at. When the current read's `revision` equals it, there are no changes, whatever the block hashes say. When either is absent, count changes with `changedBlocks` as today.
- "updated 3m ago" uses `modifiedAt` when present, else the opener's `updatedAt` (question 2).
- A re-read whose `revision` equals the shown document's can keep the shown document as it is, so a foreground re-read does not re-render or move the page.
- A revision that changed with no block changed shows no caption (question 1).

**S7 (after PR 35).** Phase 4's `loadDocument` (ruling 18).
- Drop the `refHost` pre-check in `loadDocument`: a remote session's document is read like a local one.
- Map `DocFileError` kind `host-unsupported` to the `elsewhere` document, so "This document is on <host>. Open it on the host to read it." stays for a host on an older build.
- A 503 (host offline) is `failed`, as any transient failure is today (question 3).

**Known limits.**
- A document over 16 MiB has no revision; the phone keeps its block-hash comparison for it.
- The web gets remote documents for free: its doc pane already asks `/doc/file` for a remote session and will now get the file.

---

## PR 34: a document read names its revision, S9 (Tasks 34.1-34.2)

**Branch:** `git fetch origin && git switch -c claude/s9-doc-revision origin/main`

**What it adds.** The raw `/doc/file` read sends the whole file's sha256 as its `ETag`, its modification time as `X-Doc-Modified-At`, and `Cache-Control: private, no-cache`, and answers `If-None-Match` with 304. The shared package reads both into `DocFileContent`. Nothing on the phone changes yet.

### Task 34.1: /doc/file names the revision it served

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/doc_serve.go` (imports; `docRevisionMaxBytes` after `docFileMaxBytes`; `handleDocFile`'s read and write; `readDocFile` replaced with `docFileRead` and the one-pass read; `writeDocFileRaw` takes the read and the request; `docRawTotalSize` replaced by `ifNoneMatchNames`)
- Create: `cmd/evener-hub/doc_open_unix.go`, `cmd/evener-hub/doc_open_other.go` (`docOpenNonblock`)
- Modify: `cmd/evener-hub/web_covtest_test.go` (delete the two `docRawTotalSize` tests and the imports only they used)
- Modify: `cmd/evener-hub/cov_small_faults_pass5_fuzz_test.go`, `cmd/evener-hub/cov_threadread_images_fuzz_test.go` (the coverage seeds call `readDocFile` with its root, and stub `docStat` and `docOpen` by their new types)
- Create: `cmd/evener-hub/doc_serve_revision_test.go`, `cmd/evener-hub/doc_serve_confinement_test.go`, `cmd/evener-hub/doc_serve_fifo_unix_test.go`

**Interfaces:**
- Produces (Task 35.1 and 35.2 use them):
  - `type docFileRead struct { Data []byte; TotalSize int64; Revision string; ModifiedAt time.Time }`
  - `func readDocFile(root, abs string) (docFileRead, error)`: `abs` is the path `fspaths.ResolveInRoot(root, rel)` returned
  - `func openDocInRoot(root, abs string) (*os.File, error)`, with the seams `var docOpen = openDocInRoot` and `var docStat = (*os.File).Stat`
  - Test helper `docTestRoot(t) string` (a temp folder by its real path)
  - `func writeDocFileRaw(w http.ResponseWriter, r *http.Request, doc docFileRead)`
  - `const docRevisionMaxBytes = 16 * 1024 * 1024`
  - Test helpers `docRevisionOf(content []byte) string` and `writeDocAt(t, path, content, modified)` (it creates parent directories; Task 35.1's tests write into `plans/`).

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/doc_serve_revision_test.go`:

```go
package hub

// Tests for a document read's revision identity (S9): the raw /doc/file read
// names the version it served, so the phone can tell "changed since you last
// read" from "the same file", and a client that already holds that version
// can revalidate it without the bytes.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func docRevisionOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// docTestRoot is a fresh session folder by its real path: readDocFile takes a
// path fspaths.ResolveInRoot has already symlink-resolved, and on macOS
// t.TempDir sits under /var, a symlink to /private/var.
func docTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeDocAt(t *testing.T, path string, content []byte, modified time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func docRawRequestIfNoneMatch(t *testing.T, web *WebServer, session, path, etag string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/doc/file?format=raw&session="+session+"&path="+path, nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	return rec
}

// The revision is the sha256 of the whole file, sent as a strong ETag, with
// the file's modification time in Unix milliseconds beside it. The response
// must be revalidated before reuse, so no cache serves an old version.
func TestDocFile_Raw_NamesTheRevisionItServed(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	content := []byte("# Plan\n\nStep one.\n")
	modified := time.UnixMilli(1_790_000_000_123)
	writeDocAt(t, filepath.Join(cwd, "plan.md"), content, modified)

	rec := docRawRequest(t, web, session, "plan.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("ETag"), `"`+docRevisionOf(content)+`"`; got != want {
		t.Fatalf("ETag=%q, want %q (the whole file's sha256)", got, want)
	}
	if got, want := rec.Header().Get("X-Doc-Modified-At"), strconv.FormatInt(modified.UnixMilli(), 10); got != want {
		t.Fatalf("X-Doc-Modified-At=%q, want %q", got, want)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control=%q, want private, no-cache", got)
	}
}

// An unchanged file keeps its revision, and any edit changes it, including an
// edit past the 512 KiB the read serves: two files whose served heads are
// identical still differ in revision.
func TestDocFile_Raw_RevisionFollowsTheWholeFile(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	path := filepath.Join(cwd, "long.md")
	modified := time.UnixMilli(1_790_000_000_000)
	head := bytes.Repeat([]byte("a"), docFileMaxBytes)
	writeDocAt(t, path, append(append([]byte{}, head...), "tail one"...), modified)

	first := docRawRequest(t, web, session, "long.md")
	again := docRawRequest(t, web, session, "long.md")
	if first.Header().Get("ETag") == "" || first.Header().Get("ETag") != again.Header().Get("ETag") {
		t.Fatalf("unchanged file ETags %q then %q, want one stable revision", first.Header().Get("ETag"), again.Header().Get("ETag"))
	}

	writeDocAt(t, path, append(append([]byte{}, head...), "tail two"...), modified)
	edited := docRawRequest(t, web, session, "long.md")
	if !bytes.Equal(first.Body.Bytes(), edited.Body.Bytes()) {
		t.Fatal("the served heads differ; the edit must sit past the cap for this test to mean anything")
	}
	if edited.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Fatalf("an edit past the cap kept revision %q", first.Header().Get("ETag"))
	}
}

// A client that sends the revision it holds gets 304 with no body while the
// file is unchanged, and the new version once it changes.
func TestDocFile_Raw_IfNoneMatchRevalidates(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	path := filepath.Join(cwd, "notes.txt")
	writeDocAt(t, path, []byte("first"), time.UnixMilli(1_790_000_000_000))
	etag := docRawRequest(t, web, session, "notes.txt").Header().Get("ETag")

	for _, header := range []string{etag, `"other", ` + etag, "W/" + etag, "*"} {
		rec := docRawRequestIfNoneMatch(t, web, session, "notes.txt", header)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag {
			t.Fatalf("If-None-Match %s: status=%d body=%q ETag=%q, want 304, empty, %s", header, rec.Code, rec.Body.String(), rec.Header().Get("ETag"), etag)
		}
	}

	writeDocAt(t, path, []byte("second"), time.UnixMilli(1_790_000_060_000))
	rec := docRawRequestIfNoneMatch(t, web, session, "notes.txt", etag)
	if rec.Code != http.StatusOK || rec.Body.String() != "second" {
		t.Fatalf("after an edit: status=%d body=%q, want 200 with the new version", rec.Code, rec.Body.String())
	}
}

// Hashing is bounded: a file past docRevisionMaxBytes is still served (its
// head, truncated) but names no revision, so a client falls back to comparing
// what it was shown.
func TestDocFile_Raw_NoRevisionPastTheHashLimit(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	f, err := os.Create(filepath.Join(cwd, "huge.log"))
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file: the size is real, the disk use is not.
	if err := f.Truncate(docRevisionMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	rec := docRawRequest(t, web, session, "huge.log")
	if rec.Code != http.StatusOK || rec.Body.Len() != docFileMaxBytes {
		t.Fatalf("status=%d served=%d, want 200 with the %d-byte head", rec.Code, rec.Body.Len(), docFileMaxBytes)
	}
	if got := rec.Header().Get("ETag"); got != "" {
		t.Fatalf("ETag=%q, want none past the hash limit", got)
	}
	if got := rec.Header().Get("X-Doc-Total-Size"); got != strconv.Itoa(docRevisionMaxBytes+1) {
		t.Fatalf("X-Doc-Total-Size=%q, want %d", got, docRevisionMaxBytes+1)
	}
	if rec.Header().Get("X-Doc-Modified-At") == "" {
		t.Fatal("X-Doc-Modified-At missing; the time is known at any size")
	}
	// "*" matches any current version, with or without a revision.
	if star := docRawRequestIfNoneMatch(t, web, session, "huge.log", "*"); star.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match * on an unhashed file: status=%d, want 304", star.Code)
	}
	if other := docRawRequestIfNoneMatch(t, web, session, "huge.log", `"other"`); other.Code != http.StatusOK {
		t.Fatalf("If-None-Match naming another tag on an unhashed file: status=%d, want 200", other.Code)
	}
}

// A modification time at or before the Unix epoch is not sent: the client
// reads a missing or non-positive time as no information, and the two agree.
func TestDocFile_Raw_NoTimeAtOrBeforeTheEpoch(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	writeDocAt(t, filepath.Join(cwd, "old.txt"), []byte("old"), time.UnixMilli(-1000))

	rec := docRawRequest(t, web, session, "old.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Header().Get("X-Doc-Modified-At"); got != "" {
		t.Fatalf("X-Doc-Modified-At=%q, want none for a pre-epoch time", got)
	}
}

// A file of exactly docRevisionMaxBytes is still hashed: the limit is inclusive.
func TestDocFile_Raw_RevisionAtTheHashLimit(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	path := filepath.Join(cwd, "limit.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(docRevisionMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	rec := docRawRequest(t, web, session, "limit.log")
	if got, want := rec.Header().Get("ETag"), `"`+docRevisionOf(make([]byte, docRevisionMaxBytes))+`"`; got != want {
		t.Fatalf("ETag=%q, want %q at exactly the limit", got, want)
	}
}

// A file that grows past the hash limit while it is read has no revision, and
// its size is at least what the read found, never the smaller size the stat
// saw before the open. The stat is stubbed to report the file as it was a
// moment earlier, which is the order a concurrent writer produces.
func TestReadDocFile_GrowingPastTheHashLimitReportsWhatWasRead(t *testing.T) {
	path := filepath.Join(docTestRoot(t), "growing.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(docRevisionMaxBytes + 100); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(t.TempDir(), "earlier.log")
	writeDocAt(t, small, []byte("short"), time.UnixMilli(1_790_000_000_000))
	oldStat := docStat
	t.Cleanup(func() { docStat = oldStat })
	docStat = func(*os.File) (os.FileInfo, error) { return os.Stat(small) }

	read, err := readDocFile(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	if read.Revision != "" {
		t.Fatalf("Revision=%q, want none for a file that grew past the limit", read.Revision)
	}
	if read.TotalSize <= docRevisionMaxBytes {
		t.Fatalf("TotalSize=%d, want more than the %d bytes the read found", read.TotalSize, docRevisionMaxBytes)
	}
}

// A file that shrank below the hash limit after the stat is hashed, and its
// size is what the read found: the stat's larger size never decides the
// revision.
func TestReadDocFile_ShrinkingBelowTheHashLimitIsHashed(t *testing.T) {
	content := []byte("short now")
	path := filepath.Join(docTestRoot(t), "shrunk.log")
	writeDocAt(t, path, content, time.UnixMilli(1_790_000_000_000))
	big := filepath.Join(t.TempDir(), "earlier.log")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(docRevisionMaxBytes + 100); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	oldStat := docStat
	t.Cleanup(func() { docStat = oldStat })
	docStat = func(*os.File) (os.FileInfo, error) { return os.Stat(big) }

	read, err := readDocFile(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	if read.Revision != docRevisionOf(content) || read.TotalSize != int64(len(content)) {
		t.Fatalf("Revision/TotalSize = %q/%d, want %q/%d from the bytes read", read.Revision, read.TotalSize, docRevisionOf(content), len(content))
	}
}

// An empty file is a document with nothing in it, not a missing one.
func TestDocFile_Raw_EmptyFileIsServedEmpty(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	writeDocAt(t, filepath.Join(cwd, "empty.md"), nil, time.UnixMilli(1_790_000_000_000))

	rec := docRawRequest(t, web, session, "empty.md")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q, want 200 and empty", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("ETag"), `"`+docRevisionOf(nil)+`"`; got != want {
		t.Fatalf("ETag=%q, want %q", got, want)
	}
}
```

`cmd/evener-hub/doc_serve_confinement_test.go`:

```go
package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
)

// The read itself is confined to the session's folder, not only the check
// before it: a path that passed fspaths.ResolveInRoot and was then swapped for
// a symlink leading out must not be read. The test hands readDocFile the
// swapped path directly, which is the state a swap between the check and the
// open leaves behind.
func TestReadDocFile_OpenStaysInsideTheRootAfterASwap(t *testing.T) {
	root := docTestRoot(t)
	outside := t.TempDir()
	writeDocAt(t, filepath.Join(outside, "secret.txt"), []byte("secret"), time.UnixMilli(1_790_000_000_000))
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "notes.txt")); err != nil {
		t.Fatal(err)
	}

	read, err := readDocFile(root, filepath.Join(root, "notes.txt"))
	if err == nil {
		t.Fatalf("read %q through a symlink leading out of the root, want a refusal", read.Data)
	}
}

// A symlink that stays inside the folder is still followed: ResolveInRoot
// resolves it, and the open reads the file it names.
func TestReadDocFile_FollowsASymlinkInsideTheRoot(t *testing.T) {
	root := docTestRoot(t)
	writeDocAt(t, filepath.Join(root, "docs", "plan.md"), []byte("# Plan"), time.UnixMilli(1_790_000_000_000))
	if err := os.Symlink(filepath.Join(root, "docs", "plan.md"), filepath.Join(root, "latest.md")); err != nil {
		t.Fatal(err)
	}
	abs, err := fspaths.ResolveInRoot(root, "latest.md")
	if err != nil {
		t.Fatal(err)
	}

	read, err := readDocFile(root, abs)
	if err != nil || string(read.Data) != "# Plan" {
		t.Fatalf("readDocFile = %q, %v; want the linked file", read.Data, err)
	}
}
```

`cmd/evener-hub/doc_serve_fifo_unix_test.go`:

```go
//go:build unix

package hub

import (
	"path/filepath"
	"syscall"
	"testing"
)

// A FIFO in the session's folder is refused without blocking: the open does
// not wait for a writer, and the descriptor's own stat says it is not a
// regular file. Were the open to block, this test would hang at its deadline.
func TestReadDocFile_RefusesAFIFOWithoutBlocking(t *testing.T) {
	root := docTestRoot(t)
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}

	if read, err := readDocFile(root, fifo); err == nil {
		t.Fatalf("read %q from a FIFO, want a refusal", read.Data)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./cmd/evener-hub -run 'TestDocFile_Raw_(NamesThe|RevisionFollows|IfNoneMatch|NoRevision|RevisionAtThe|NoTimeAt|EmptyFile)|TestReadDocFile_' -count=1`
Expected: build failure, `undefined: docRevisionMaxBytes`. Once the constant alone exists, the `/doc/file` tests fail on the missing `ETag` (and `TestDocFile_Raw_NoTimeAtOrBeforeTheEpoch` passes vacuously until the header exists; it goes red if the header is sent for a pre-epoch time), `TestDocFile_Raw_EmptyFileIsServedEmpty` fails with status 404, and the `TestReadDocFile_` tests fail to build until `readDocFile` takes its root and returns a `docFileRead`. Once they build against the new `readDocFile` with a plain `os.OpenFile(abs, ...)` in place of the `os.Root` open, `TestReadDocFile_OpenStaysInsideTheRootAfterASwap` fails with `read "secret" through a symlink leading out of the root`. `TestReadDocFile_RefusesAFIFOWithoutBlocking` pins behavior that must not change (today's stat-first read refuses a FIFO too): it would hang if the open blocked.

- [ ] **Step 3: Read the file once, and write what the read found**

In `cmd/evener-hub/doc_serve.go`: the imports gain `crypto/sha256`, `encoding/hex`, `io` and `time`; `docRevisionMaxBytes` goes after `docFileMaxBytes`; `handleDocFile` keeps the read as `doc` and passes it with the request; `readDocFile` becomes the one-pass read; `writeDocFileRaw` takes the read and answers `If-None-Match`; `docRawTotalSize` is deleted and `ifNoneMatchNames` takes its place.

`cmd/evener-hub/doc_serve.go`:

```diff
diff --git a/cmd/evener-hub/doc_serve.go b/cmd/evener-hub/doc_serve.go
--- a/cmd/evener-hub/doc_serve.go
+++ b/cmd/evener-hub/doc_serve.go
@@ -2,26 +2,39 @@ package hub
 
 import (
 	"bytes"
+	"crypto/sha256"
+	"encoding/hex"
 	"errors"
+	"io"
 	"net/http"
 	"os"
 	"path/filepath"
 	"strconv"
 	"strings"
+	"time"
 
 	"primeradiant.com/evener/appwire"
 	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
 	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
 )
 
-var docStat = os.Stat
-var docOpen = os.Open
+// docOpen opens a document for reading, confined to its session folder, and
+// docStat stats the open file. Both are variables so coverage tests can fail
+// them.
+var docOpen = openDocInRoot
+var docStat = (*os.File).Stat
 
 // docFileMaxBytes caps how much of a file we read into a document pane. A pane
 // is a quick read-only reference, not a pager; large files are truncated with
 // a notice rather than streamed in full.
 const docFileMaxBytes = 512 * 1024
 
+// docRevisionMaxBytes bounds how much of a file a read hashes for its revision.
+// Hashing costs a full read of the file (about 7 ms for 16 MiB, measured with
+// sha256 on an M4 Max); the largest markdown file measured across 66,387 in
+// ~/git was 3.2 MB. A larger file is served without a revision.
+const docRevisionMaxBytes = 16 * 1024 * 1024
+
 // handleDocFile serves a LOCAL session file's literal bytes for the React
 // doc-viewer pane, which renders the content itself. The route has a single
 // mode, ?format=raw; a request that omits format or sends any other value is a
@@ -67,7 +80,7 @@ func (s *WebServer) handleDocFile(w http.ResponseWriter, r *http.Request) {
 		return
 	}
 
-	data, err := readDocFile(abs)
+	doc, err := readDocFile(cwd, abs)
 	if err != nil {
 		http.NotFound(w, r)
 		return
@@ -77,7 +90,7 @@ func (s *WebServer) handleDocFile(w http.ResponseWriter, r *http.Request) {
 		http.Error(w, "format=raw required", http.StatusBadRequest)
 		return
 	}
-	writeDocFileRaw(w, data, docRawTotalSize(abs, len(data)))
+	writeDocFileRaw(w, r, doc)
 }
 
 // handleDocImage serves a validated image file inside a session's working
@@ -171,27 +184,83 @@ func sessionCWD(cfg hubcore.WebConfig, session string) (string, bool) {
 	return "", false
 }
 
-// readDocFile reads up to docFileMaxBytes from a regular file. Directories and
-// other non-regular files are refused.
-func readDocFile(abs string) ([]byte, error) {
-	info, err := docStat(abs)
+// docFileRead is one read of a document: the head a pane shows and what the
+// whole file is. Revision is the lowercase hex sha256 of the whole file, empty
+// when the file is larger than docRevisionMaxBytes. TotalSize and Revision come
+// from the same pass over the file, so they describe one version even while the
+// file is being written.
+type docFileRead struct {
+	Data       []byte
+	TotalSize  int64
+	Revision   string
+	ModifiedAt time.Time
+}
+
+// readDocFile reads the first docFileMaxBytes of abs, a path
+// fspaths.ResolveInRoot accepted for root, with the file's size and revision.
+// The open goes through root again (openDocInRoot), so a symlink swapped in
+// after the check cannot lead it out, and it does not wait on a FIFO. The
+// stat is of the open file, so directories and other non-regular files are
+// refused whatever the path names by then.
+func readDocFile(root, abs string) (docFileRead, error) {
+	f, err := docOpen(root, abs)
 	if err != nil {
-		return nil, err
-	}
-	if !info.Mode().IsRegular() {
-		return nil, os.ErrInvalid
-	}
-	f, err := docOpen(abs)
-	if err != nil {
-		return nil, err
+		return docFileRead{}, err
 	}
 	defer f.Close() //nolint:errcheck // read-only file; close error is not actionable
-	buf := make([]byte, docFileMaxBytes)
-	n, err := f.Read(buf)
-	if err != nil && n == 0 {
+	info, err := docStat(f)
+	if err != nil {
+		return docFileRead{}, err
+	}
+	if !info.Mode().IsRegular() {
+		return docFileRead{}, os.ErrInvalid
+	}
+	head := make([]byte, docFileMaxBytes)
+	n, err := io.ReadFull(f, head)
+	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
+		return docFileRead{}, err
+	}
+	read := docFileRead{Data: head[:n], TotalSize: info.Size(), ModifiedAt: info.ModTime()}
+	// The bytes read, never the stat's size, decide the revision: the file
+	// may have grown or shrunk since the stat. Reading stops one byte past the
+	// limit, so a huge file costs at most docRevisionMaxBytes of hashing.
+	hash := sha256.New()
+	hash.Write(read.Data)
+	rest, err := io.Copy(hash, io.LimitReader(f, docRevisionMaxBytes-int64(n)+1))
+	if err != nil {
+		return docFileRead{}, err
+	}
+	total := int64(n) + rest
+	if total > docRevisionMaxBytes {
+		// Too large to hash: no revision, and a size of at least what the
+		// read found.
+		read.TotalSize = max(read.TotalSize, total)
+		return read, nil
+	}
+	read.TotalSize, read.Revision = total, hex.EncodeToString(hash.Sum(nil))
+	return read, nil
+}
+
+// openDocInRoot opens abs for reading through an os.Root at root, which
+// refuses any path, symlinks included, that resolves outside root at the
+// moment of the open. abs is the symlink-resolved path ResolveInRoot returned,
+// so it is expressed relative to the symlink-resolved root. docOpenNonblock
+// keeps the open from waiting on a FIFO; a regular file reads the same.
+func openDocInRoot(root, abs string) (*os.File, error) {
+	realRoot, err := filepath.EvalSymlinks(root)
+	if err != nil {
 		return nil, err
 	}
-	return buf[:n], nil
+	rel, err := filepath.Rel(realRoot, abs)
+	if err != nil {
+		return nil, err
+	}
+	dir, err := os.OpenRoot(realRoot)
+	if err != nil {
+		return nil, err
+	}
+	defer dir.Close() //nolint:errcheck // a file opened through it stays open
+	return dir.OpenFile(rel, os.O_RDONLY|docOpenNonblock, 0)
 }
 
 // looksBinaryBytes reports whether a byte slice looks like binary content. A
@@ -216,34 +285,59 @@ func looksBinaryBytes(data []byte) bool {
 // application/octet-stream are both honest about the content and never
 // browser-executable.
 //
-// data is capped at docFileMaxBytes; totalSize is the file's true byte size.
-// When the file is larger than the cap the body is only its head, so an
+// doc.Data is capped at docFileMaxBytes; doc.TotalSize is the file's true byte
+// size. When the file is larger than the cap the body is only its head, so an
 // explicit X-Doc-Truncated / X-Doc-Total-Size pair lets the pane render an
 // exact notice instead of inferring truncation from the body length (which is
 // ambiguous at exactly the cap). A file of exactly the cap size is complete,
 // hence not truncated.
-func writeDocFileRaw(w http.ResponseWriter, data []byte, totalSize int64) {
-	if looksBinaryBytes(data) {
+//
+// The revision rides as a strong ETag and the modification time as
+// X-Doc-Modified-At (Unix milliseconds). "no-cache" makes every cache
+// revalidate before reuse, and a request whose If-None-Match names the
+// revision is answered 304 without the body.
+func writeDocFileRaw(w http.ResponseWriter, r *http.Request, doc docFileRead) {
+	w.Header().Set("Cache-Control", "private, no-cache")
+	if ms := docModifiedMillis(doc.ModifiedAt); ms != 0 {
+		w.Header().Set("X-Doc-Modified-At", strconv.FormatInt(ms, 10))
+	}
+	etag := ""
+	if doc.Revision != "" {
+		etag = `"` + doc.Revision + `"`
+		w.Header().Set("ETag", etag)
+	}
+	if ifNoneMatchNames(r.Header.Get("If-None-Match"), etag) {
+		w.WriteHeader(http.StatusNotModified)
+		return
+	}
+	if looksBinaryBytes(doc.Data) {
 		w.Header().Set("Content-Type", "application/octet-stream")
 	} else {
 		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
 	}
-	if totalSize > docFileMaxBytes {
+	if doc.TotalSize > docFileMaxBytes {
 		w.Header().Set("X-Doc-Truncated", "true")
-		w.Header().Set("X-Doc-Total-Size", strconv.FormatInt(totalSize, 10))
+		w.Header().Set("X-Doc-Total-Size", strconv.FormatInt(doc.TotalSize, 10))
 	}
-	_, _ = w.Write(data)
+	_, _ = w.Write(doc.Data)
 }
 
-// docRawTotalSize returns the file's true byte size for the raw pane's
-// truncation signal. It re-stats the file rather than threading a size out of
-// readDocFile, which the HTML variant shares and this raw-only change must not
-// disturb. On a stat error — unlikely, the file was readable a moment ago — it
-// falls back to the bytes actually read, which reads as "not truncated": an
-// honest degrade to the earlier no-signal behavior.
-func docRawTotalSize(abs string, read int) int64 {
-	if info, err := docStat(abs); err == nil {
-		return info.Size()
-	}
-	return int64(read)
+// docModifiedMillis is a modification time in Unix milliseconds, or 0 for a
+// time at or before the epoch, which is sent as no time at all.
+func docModifiedMillis(modified time.Time) int64 {
+	return max(modified.UnixMilli(), 0)
+}
+
+// ifNoneMatchNames reports whether an If-None-Match header lists etag, or is
+// "*", which matches any current version, even one with no revision (an empty
+// etag). The comparison is weak, as RFC 9110 section 13.1.2 has it: a W/
+// prefix on a listed tag is ignored.
+func ifNoneMatchNames(header, etag string) bool {
+	for listed := range strings.SplitSeq(header, ",") {
+		listed = strings.TrimSpace(listed)
+		if listed == "*" || (etag != "" && strings.TrimPrefix(listed, "W/") == etag) {
+			return true
+		}
+	}
+	return false
 }
```

`cmd/evener-hub/doc_open_unix.go`:

```go
//go:build unix

package hub

import "syscall"

// docOpenNonblock keeps a document open from waiting for a FIFO's writer.
const docOpenNonblock = syscall.O_NONBLOCK
```

`cmd/evener-hub/doc_open_other.go`:

```go
//go:build !unix

package hub

// docOpenNonblock is zero where there are no FIFOs to wait on.
const docOpenNonblock = 0
```

The coverage seeds follow the new signatures:

`cmd/evener-hub/cov_small_faults_pass5_fuzz_test.go`:

```diff
diff --git a/cmd/evener-hub/cov_small_faults_pass5_fuzz_test.go b/cmd/evener-hub/cov_small_faults_pass5_fuzz_test.go
--- a/cmd/evener-hub/cov_small_faults_pass5_fuzz_test.go
+++ b/cmd/evener-hub/cov_small_faults_pass5_fuzz_test.go
@@ -109,17 +109,17 @@ func FuzzSmallFaultsPass5(f *testing.F) {
 		call(web.handleDocImage, http.MethodGet, "/doc/image?session=01PASS5&path=.")
 		call(web.handleDocImage, http.MethodGet, "/doc/image?session=remote:x&path=out.png")
 		call(web.handleDocImage, http.MethodPost, "/doc/image")
-		_, _ = readDocFile(cwd)
-		_, _ = readDocFile(filepath.Join(cwd, "missing"))
+		_, _ = readDocFile(cwd, cwd)
+		_, _ = readDocFile(cwd, filepath.Join(cwd, "missing"))
 		_ = looksBinaryBytes(append(make([]byte, 9000), 0))
-		docStat = func(string) (os.FileInfo, error) { return nil, errors.New("stat") }
-		_, _ = readDocFile("x")
+		docStat = func(*os.File) (os.FileInfo, error) { return nil, errors.New("stat") }
+		_, _ = readDocFile(cwd, filepath.Join(cwd, "note.txt"))
 		docStat = oldStat
-		docOpen = func(string) (*os.File, error) { return nil, errors.New("open") }
-		_, _ = readDocFile(filepath.Join(cwd, "note.txt"))
+		docOpen = func(string, string) (*os.File, error) { return nil, errors.New("open") }
+		_, _ = readDocFile(cwd, filepath.Join(cwd, "note.txt"))
 		docOpen = oldOpen
-		docOpen = func(string) (*os.File, error) { return os.Open(cwd) }
-		_, _ = readDocFile(filepath.Join(cwd, "note.txt"))
+		docOpen = func(string, string) (*os.File, error) { return os.Open(cwd) }
+		_, _ = readDocFile(cwd, filepath.Join(cwd, "note.txt"))
 		docOpen = oldOpen
 		_, _ = web.localSessionCWD("remote:x")
 		_, _ = web.localSessionCWD("01MISSING")
```

`cmd/evener-hub/cov_threadread_images_fuzz_test.go`:

```diff
diff --git a/cmd/evener-hub/cov_threadread_images_fuzz_test.go b/cmd/evener-hub/cov_threadread_images_fuzz_test.go
--- a/cmd/evener-hub/cov_threadread_images_fuzz_test.go
+++ b/cmd/evener-hub/cov_threadread_images_fuzz_test.go
@@ -192,12 +192,12 @@ func covDocServeSeed(t *testing.T) {
 	liveWeb := NewWebServer(hubcore.WebConfig{Roster: roster})
 	_, _ = liveWeb.localSessionCWD("live")
 	_, _ = liveWeb.localSessionCWD("missing")
-	_, _ = readDocFile(cwd)
-	_, _ = readDocFile(filepath.Join(cwd, "missing"))
+	_, _ = readDocFile(cwd, cwd)
+	_, _ = readDocFile(cwd, filepath.Join(cwd, "missing"))
 	if err := os.WriteFile(filepath.Join(cwd, "empty"), nil, 0o644); err != nil {
 		t.Fatal(err)
 	}
-	_, _ = readDocFile(filepath.Join(cwd, "empty"))
+	_, _ = readDocFile(cwd, filepath.Join(cwd, "empty"))
 	large := append(make([]byte, 8193), 0)
 	_ = looksBinaryBytes(large)
 }
```

`docRawTotalSize` had two coverage tests. Delete them (from `// --- doc_serve.go: docRawTotalSize ---` down to `// --- web_api_tree.go: resolveTopLevelSessionRef ---`, keeping that line) and the `os` and `path/filepath` imports only they used. The new tests cover the total size through the route (`TestDocFile_Raw_NoRevisionPastTheHashLimit` and the existing truncation tests).

`cmd/evener-hub/web_covtest_test.go`:

```diff
diff --git a/cmd/evener-hub/web_covtest_test.go b/cmd/evener-hub/web_covtest_test.go
--- a/cmd/evener-hub/web_covtest_test.go
+++ b/cmd/evener-hub/web_covtest_test.go
@@ -4,8 +4,6 @@ import (
 	"context"
 	"net/http"
 	"net/http/httptest"
-	"os"
-	"path/filepath"
 	"reflect"
 	"testing"
 
@@ -422,30 +420,6 @@ func TestCovSplitProviderModelNoSlash(t *testing.T) {
 	}
 }
 
-// --- doc_serve.go: docRawTotalSize ---
-
-// TestCovDocRawTotalSizeStatError covers the fallback to read size when stat
-// fails (doc_serve.go:230-231).
-func TestCovDocRawTotalSizeStatError(t *testing.T) {
-	missingPath := filepath.Join(t.TempDir(), "missing", "document.txt")
-	got := docRawTotalSize(missingPath, 42)
-	if got != 42 {
-		t.Fatalf("expected 42 (read fallback), got %d", got)
-	}
-}
-
-// TestCovDocRawTotalSizeStatOK covers the stat-success path (doc_serve.go:229).
-func TestCovDocRawTotalSizeStatOK(t *testing.T) {
-	payload := []byte("known document bytes")
-	path := filepath.Join(t.TempDir(), "doc.txt")
-	if err := os.WriteFile(path, payload, 0o600); err != nil {
-		t.Fatal(err)
-	}
-	if got, want := docRawTotalSize(path, 1), int64(len(payload)); got != want {
-		t.Fatalf("docRawTotalSize = %d, want stat size %d", got, want)
-	}
-}
-
 // --- web_api_tree.go: resolveTopLevelSessionRef ---
 
 // TestCovTopLevelFavoriteSessionIDClusterPrefix covers the cluster-prefix
```

- [ ] **Step 4: Run the tests, the old doc route tests and the coverage seeds**

Run: `go test ./cmd/evener-hub -run 'TestDocFile|TestDocImage|TestCov|TestReadDocFile' -count=1 && go test ./cmd/evener-hub -run '^(FuzzSmallFaultsPass5|FuzzCovThreadreadImagesSeed100)$' -count=1`
Expected: PASS.

Run: `go vet ./cmd/evener-hub/ && go vet -tags evenerfuzz ./cmd/evener-hub/ && GOOS=windows go vet -tags evenerfuzz ./cmd/evener-hub/ && GOOS=linux go vet -tags evenerfuzz ./cmd/evener-hub/ && golangci-lint run ./cmd/evener-hub/ && $(go env GOROOT)/bin/gofmt -l cmd/evener-hub`
Expected: no output from gofmt, `0 issues.` from the linter.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/doc_serve.go cmd/evener-hub/doc_open_unix.go cmd/evener-hub/doc_open_other.go \
  cmd/evener-hub/doc_serve_revision_test.go cmd/evener-hub/doc_serve_confinement_test.go cmd/evener-hub/doc_serve_fifo_unix_test.go \
  cmd/evener-hub/web_covtest_test.go cmd/evener-hub/cov_small_faults_pass5_fuzz_test.go cmd/evener-hub/cov_threadread_images_fuzz_test.go
git commit -m "feat(hub): /doc/file names the revision it served (S9, phase 7 PR 34)"
```

### Task 34.2: DocFileContent carries the revision and the time

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire-client/typescript/docContent.ts` (`DocFileContent` gains `revision` and `modifiedAt`; `readDocFile` reads them; new `etagRevision`)
- Modify: `appwire-client/typescript/docContent.test.ts` (four tests before the 403 test in `describe("readDocFile")`)

**Interfaces:**
- Consumes: the `ETag` and `X-Doc-Modified-At` headers from Task 34.1.
- Produces: `DocFileContent.revision?: string` (the sha256 hex, without quotes; only a strong, quoted, 64-character lowercase hex `ETag` names one, so a weak or foreign tag can never make a changed file read as unchanged) and `DocFileContent.modifiedAt?: number` (Unix milliseconds). Both keys are absent, not undefined, when the hub sends no header.

- [ ] **Step 1: Write the failing tests**

`appwire-client/typescript/docContent.test.ts`:

```diff
diff --git a/appwire-client/typescript/docContent.test.ts b/appwire-client/typescript/docContent.test.ts
--- a/appwire-client/typescript/docContent.test.ts
+++ b/appwire-client/typescript/docContent.test.ts
@@ -158,6 +158,53 @@ describe("readDocFile", () => {
     expect(doc.sizeBytes).toBe(DOC_FILE_MAX_BYTES);
   });
 
+  test("the revision comes from the ETag and the modification time from X-Doc-Modified-At (S9)", async () => {
+    const port = respondWith(
+      new Response("# Plan", {
+        headers: {
+          "Content-Type": "text/plain; charset=utf-8",
+          ETag: '"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"',
+          "X-Doc-Modified-At": "1790000000123",
+        },
+      }),
+    );
+    const doc = await readDocFile("s1", "plan.md", port);
+    expect(doc.revision).toBe("9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08");
+    expect(doc.modifiedAt).toBe(1790000000123);
+  });
+
+  test("only a strong sha256 ETag names a revision; any other tag is no information", async () => {
+    // The hub sends the whole file's sha256 as a strong tag. A weak tag, or one
+    // that is not a sha256, could name a version the file does not have, so
+    // the caller falls back to comparing what it was shown.
+    const sha = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08";
+    for (const etag of [`W/"${sha}"`, '"abc123"', sha, `"${sha.toUpperCase()}"`]) {
+      const port = respondWith(
+        new Response("x", { headers: { "Content-Type": "text/plain; charset=utf-8", ETag: etag } }),
+      );
+      expect("revision" in (await readDocFile("s1", "x.txt", port))).toBe(false);
+    }
+  });
+
+  test("a hub without S9, or a file past the hash limit, names no revision and no time", async () => {
+    // Absent keys, not empty ones: the phone's fallback keys on absence.
+    const port = respondWith(new Response("x", { headers: headers("text/plain; charset=utf-8") }));
+    const doc = await readDocFile("s1", "x.txt", port);
+    expect("revision" in doc).toBe(false);
+    expect("modifiedAt" in doc).toBe(false);
+  });
+
+  test("a malformed ETag or modification time is no information", async () => {
+    const port = respondWith(
+      new Response("x", {
+        headers: { "Content-Type": "text/plain; charset=utf-8", ETag: '""', "X-Doc-Modified-At": "yesterday" },
+      }),
+    );
+    const doc = await readDocFile("s1", "x.txt", port);
+    expect("revision" in doc).toBe(false);
+    expect("modifiedAt" in doc).toBe(false);
+  });
+
   test("a 403 (path escapes the session cwd) rejects with a forbidden DocFileError", async () => {
     const port = respondWith(new Response("forbidden", { status: 403 }));
     await expect(readDocFile("s1", "../etc/passwd", port)).rejects.toMatchObject({
```

- [ ] **Step 2: Run them to see them fail**

Run (from `cmd/evener-hub/frontend`): `npx vitest run ../../../appwire-client/typescript/docContent.test.ts`
Expected: the first new test FAILS (`revision` is undefined). The three absence tests (a tag that is not a strong sha256, no headers, malformed headers) pass already and pin that the keys stay absent.

- [ ] **Step 3: Read the headers**

`appwire-client/typescript/docContent.ts`:

```diff
diff --git a/appwire-client/typescript/docContent.ts b/appwire-client/typescript/docContent.ts
--- a/appwire-client/typescript/docContent.ts
+++ b/appwire-client/typescript/docContent.ts
@@ -23,6 +23,13 @@ export interface DocFileContent {
   // truncated it (from the X-Doc-Total-Size header). Lets the pane say
   // exactly how much was elided, not just that something was.
   totalBytes?: number;
+  // The version this read served (S9): the sha256 of the whole file, from the
+  // response's ETag. Absent from a hub without S9 and for a file too large to
+  // hash (cmd/evener-hub/doc_serve.go docRevisionMaxBytes), so a caller falls
+  // back to comparing what it was shown.
+  revision?: string;
+  // When the file was last modified, in Unix milliseconds (X-Doc-Modified-At).
+  modifiedAt?: number;
 }
 
 // The server reads at most this many bytes into a doc pane and never streams
@@ -125,7 +132,26 @@ export async function readDocFile(session: string, path: string, port: DocPort):
   const parsedTotal = totalHeader === null ? Number.NaN : Number.parseInt(totalHeader, 10);
   const totalBytes = Number.isFinite(parsedTotal) ? parsedTotal : undefined;
   const text = binary ? "" : new TextDecoder().decode(buf);
-  return { text, binary, mediaType, truncated, sizeBytes, totalBytes };
+  const revision = etagRevision(res.headers.get("ETag"));
+  const modifiedAt = Number(res.headers.get("X-Doc-Modified-At") ?? Number.NaN);
+  return {
+    text,
+    binary,
+    mediaType,
+    truncated,
+    sizeBytes,
+    totalBytes,
+    ...(revision === undefined ? {} : { revision }),
+    ...(Number.isSafeInteger(modifiedAt) && modifiedAt > 0 ? { modifiedAt } : {}),
+  };
+}
+
+// etagRevision reads the revision out of an ETag. The hub sends the whole
+// file's sha256 as a strong tag (cmd/evener-hub/doc_serve.go writeDocFileRaw),
+// so only a quoted 64-character lowercase hex tag names one; a weak tag, or any
+// other, is no information.
+function etagRevision(etag: string | null): string | undefined {
+  return /^"([0-9a-f]{64})"$/.exec(etag?.trim() ?? "")?.[1];
 }
 
 // docImageURL builds the /doc/image href for a session-scoped, cwd-relative
```

- [ ] **Step 4: Run the package's tests, the typecheck and Biome**

Run (from `cmd/evener-hub/frontend`): `npx vitest run ../../../appwire-client/typescript/ && npm run typecheck && npx biome check --write ../../../appwire-client/typescript/docContent.ts ../../../appwire-client/typescript/docContent.test.ts`
Expected: PASS, no type errors, Biome clean.

- [ ] **Step 5: Commit**

```bash
git add appwire-client/typescript/docContent.ts appwire-client/typescript/docContent.test.ts
git commit -m "feat(sdk): a document read carries its revision and time (S9, phase 7 PR 34)"
```

**PR.** Title "feat: a document read names its revision (S9, phase 7 PR 34)". The body names the headers and what each is for, rulings 1 to 6 (whole-file hash, the 16 MiB limit with its measurement, one pass, milliseconds rather than `Last-Modified`, `no-cache`, the empty-file fix), and the phone lane's S9 switch-over.

---

## PR 35: documents in remote sessions, S7 (Tasks 35.1-35.3)

**Branch:** `git fetch origin && git switch -c claude/s7-remote-documents origin/main` (after PR 34 has merged)

**What it adds.** `evener/session/document` on every hub, the controller's `/doc/file` proxy for a host-qualified session, and the `host-unsupported` failure kind. The web's doc pane shows remote documents with no further change.

### Task 35.1: every hub reads its own session documents over AppWire

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire/types.go` (`MethodEvenerSessionDocument` after `MethodEvenerSessionImage`; `SessionDocumentParams` and `SessionDocumentResponse` before `ThreadTranscriptListParams`)
- Modify: `appwire/errors.go` (`ErrorPathOutsideSession` after `ErrorUpgradeRequired`; `PathOutsideSession` before `ResourceNotFound`)
- Modify: `appwire/protocol.go` (the catalog row after `MethodEvenerSessionImage`'s)
- Modify: `cmd/evener-hub/doc_serve.go` (`sessionDocumentFromHub` before `handleDocImage`)
- Modify: `cmd/evener-hub/app_rpc.go` (registration after `evener/session/image`'s, in `registerThreadHandlers`)
- Modify: `cmd/evener-hub/app_host_admin_test.go` (the deny row and its comment), `cmd/evener-hub/app_rpc_test.go` (the handler set)
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`
- Create: `cmd/evener-hub/app_rpc_session_document_test.go`

**Interfaces:**
- Consumes: `readDocFile` and `docFileRead` (Task 34.1); `sessionCWD` and `canonicalRouteID` (existing); the test helpers `writeDocAt`, `docRevisionOf` (Task 34.1), `seedSessionImageSession`, `sessionImageTestSession`, `sessionImageTestPNG`, `sessionImageErrorInfo` (`app_rpc_session_image_test.go`), `newHubRPCTestServerWithWeb` and `dialHubRPC` (`app_rpc_test.go`).
- Produces (Task 35.2 uses them):
  - `appwire.MethodEvenerSessionDocument = "evener/session/document"`
  - `appwire.SessionDocumentParams{SessionID, Path}`, `appwire.SessionDocumentResponse{Data, TotalSize, Revision, ModifiedAt}`
  - `appwire.ErrorPathOutsideSession = "pathOutsideSession"`, `func appwire.PathOutsideSession(message string) WireError`
  - `func sessionDocumentFromHub(cfg hubcore.WebConfig, params appwire.SessionDocumentParams) (appwire.SessionDocumentResponse, error)`
  - Test helpers `requestSessionDocument(t, srv, params)` and `sessionDocumentServer(t, cwd) *httptest.Server`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/app_rpc_session_document_test.go`:

```go
package hub

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func requestSessionDocument(t *testing.T, srv *httptest.Server, params appwire.SessionDocumentParams) (appwire.SessionDocumentResponse, error) {
	t.Helper()
	rpc := dialHubRPC(t, srv)
	defer rpc.Close()
	if _, err := rpc.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var resp appwire.SessionDocumentResponse
	err := rpc.Request(context.Background(), appwire.MethodEvenerSessionDocument, params, &resp)
	return resp, err
}

// sessionDocumentServer serves a hub whose one past session works in cwd.
func sessionDocumentServer(t *testing.T, cwd string) *httptest.Server {
	t.Helper()
	past := seedSessionImageSession(t, cwd, sessionImageTestPNG, "image/png")
	srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
	t.Cleanup(srv.Close)
	return srv
}

// The host-side evener/session/document is the AppWire counterpart of the
// local raw /doc/file read: the same head, total size, revision and
// modification time, from the host's own session folder.
func TestHubSessionDocumentServesTheLocalRead(t *testing.T) {
	cwd := t.TempDir()
	content := []byte("# Plan\n\nStep one.\n")
	modified := time.UnixMilli(1_790_000_000_123)
	writeDocAt(t, filepath.Join(cwd, "plans", "plan.md"), content, modified)
	srv := sessionDocumentServer(t, cwd)

	resp, err := requestSessionDocument(t, srv, appwire.SessionDocumentParams{
		SessionID: sessionImageTestSession,
		Path:      "plans/plan.md",
	})
	if err != nil {
		t.Fatalf("evener/session/document: %v", err)
	}
	if !bytes.Equal(resp.Data, content) || resp.TotalSize != int64(len(content)) {
		t.Fatalf("Data/TotalSize = %q/%d, want the whole file", resp.Data, resp.TotalSize)
	}
	if resp.Revision != docRevisionOf(content) || resp.ModifiedAt != modified.UnixMilli() {
		t.Fatalf("Revision/ModifiedAt = %q/%d, want %q/%d", resp.Revision, resp.ModifiedAt, docRevisionOf(content), modified.UnixMilli())
	}
}

// A file over the cap comes back as its head and its true size, as the local
// route serves it.
func TestHubSessionDocumentSendsTheHeadOfALargeFile(t *testing.T) {
	cwd := t.TempDir()
	content := bytes.Repeat([]byte("a"), docFileMaxBytes+100)
	writeDocAt(t, filepath.Join(cwd, "big.log"), content, time.UnixMilli(1_790_000_000_000))
	srv := sessionDocumentServer(t, cwd)

	resp, err := requestSessionDocument(t, srv, appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: "big.log"})
	if err != nil {
		t.Fatalf("evener/session/document: %v", err)
	}
	if len(resp.Data) != docFileMaxBytes || resp.TotalSize != int64(len(content)) || resp.Revision != docRevisionOf(content) {
		t.Fatalf("len(Data)=%d TotalSize=%d Revision=%q, want the %d-byte head of %d and the whole file's revision",
			len(resp.Data), resp.TotalSize, resp.Revision, docFileMaxBytes, len(content))
	}
}

// The method reads only inside the named session's own folder on this hub,
// by the local route's own rule (fspaths.ResolveInRoot): a path that climbs
// out, an absolute path outside the folder, and a symlink that leads out are
// refused as pathOutsideSession; a session this hub does not have, including
// one named on another host, and a missing file are resourceNotFound.
func TestHubSessionDocumentStaysInsideTheSessionFolder(t *testing.T) {
	// The folder's real path: on macOS t.TempDir sits under /var, a symlink to
	// /private/var, and an absolute path is compared with the resolved folder.
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeDocAt(t, filepath.Join(outside, "secret.txt"), []byte("secret"), time.UnixMilli(1_790_000_000_000))
	writeDocAt(t, filepath.Join(cwd, "notes.txt"), []byte("notes"), time.UnixMilli(1_790_000_000_000))
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(cwd, "link.txt")); err != nil {
		t.Fatal(err)
	}
	srv := sessionDocumentServer(t, cwd)

	// An absolute path inside the folder is the same file, as on /doc/file.
	inside, err := requestSessionDocument(t, srv, appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: filepath.Join(cwd, "notes.txt")})
	if err != nil || string(inside.Data) != "notes" {
		t.Fatalf("absolute path inside the folder = %q, %v; want notes", inside.Data, err)
	}

	for _, tc := range []struct {
		name   string
		params appwire.SessionDocumentParams
		want   appwire.ErrorInfo
	}{
		{"no session", appwire.SessionDocumentParams{Path: "notes.txt"}, appwire.ErrorInvalidParams},
		{"no path", appwire.SessionDocumentParams{SessionID: sessionImageTestSession}, appwire.ErrorInvalidParams},
		{"dot-dot", appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: "../" + filepath.Base(outside) + "/secret.txt"}, appwire.ErrorPathOutsideSession},
		{"absolute outside", appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: filepath.Join(outside, "secret.txt")}, appwire.ErrorPathOutsideSession},
		{"symlink out", appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: "link.txt"}, appwire.ErrorPathOutsideSession},
		{"unknown session", appwire.SessionDocumentParams{SessionID: "01NOPE", Path: "notes.txt"}, appwire.ErrorResourceNotFound},
		{"another host's session", appwire.SessionDocumentParams{SessionID: "h2:" + sessionImageTestSession, Path: "notes.txt"}, appwire.ErrorResourceNotFound},
		{"missing file", appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: "gone.txt"}, appwire.ErrorResourceNotFound},
		{"a directory", appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: "."}, appwire.ErrorResourceNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := requestSessionDocument(t, srv, tc.params)
			if err == nil {
				t.Fatalf("served %q, want a %s refusal", resp.Data, tc.want)
			}
			if got := sessionImageErrorInfo(t, err); got != string(tc.want) {
				t.Fatalf("refusal = %q (%v), want %q", got, err, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./cmd/evener-hub -run 'TestHubSessionDocument' -count=1`
Expected: build failure, `undefined: appwire.SessionDocumentParams`.

- [ ] **Step 3: Add the wire types, the error and the catalog row**

`appwire/types.go`:

```diff
diff --git a/appwire/types.go b/appwire/types.go
--- a/appwire/types.go
+++ b/appwire/types.go
@@ -251,6 +251,11 @@ const (
 	// another source, so bytes stamped by a remote hub never resolve against
 	// the controller's filesystem. See SessionImageParams.
 	MethodEvenerSessionImage = "evener/session/image"
+	// MethodEvenerSessionDocument reads one document out of the recipient hub's
+	// own local session state (S7): the controller's /doc/file proxies through
+	// it when the session id names another source, as the image routes do
+	// through MethodEvenerSessionImage. See SessionDocumentParams.
+	MethodEvenerSessionDocument = "evener/session/document"
 )
 
 const (
@@ -2198,6 +2203,28 @@ type SessionImageResponse struct {
 	Data      []byte `json:"data"`
 }
 
+// SessionDocumentParams names one file in a session's working directory on the
+// recipient hub. SessionID names the session in the recipient's own namespace
+// and is never a routing field for another source. Path is resolved exactly as
+// the local /doc/file route resolves it: relative to the session's working
+// directory, or absolute inside it, and refused when it or a symlink along it
+// leads outside.
+type SessionDocumentParams struct {
+	SessionID string `json:"sessionId"`
+	Path      string `json:"path"`
+}
+
+// SessionDocumentResponse is one read of a session document: at most the first
+// 512 KiB of the file (base64 inside the JSON frame), the file's true size, the
+// lowercase hex sha256 of the whole file (absent for a file too large to hash),
+// and its modification time in Unix milliseconds.
+type SessionDocumentResponse struct {
+	Data       []byte `json:"data"`
+	TotalSize  int64  `json:"totalSize"`
+	Revision   string `json:"revision,omitempty"`
+	ModifiedAt int64  `json:"modifiedAt,omitempty"`
+}
+
 type ThreadTranscriptListParams struct {
 	Ref string `json:"ref"`
 }
```

`appwire/errors.go`:

```diff
diff --git a/appwire/errors.go b/appwire/errors.go
--- a/appwire/errors.go
+++ b/appwire/errors.go
@@ -45,6 +45,11 @@ const (
 	// an older AppWire protocol than the server speaks; the message names both
 	// versions.
 	ErrorUpgradeRequired ErrorInfo = "upgradeRequired"
+	// ErrorPathOutsideSession marks a file read whose path, or a symlink along
+	// it, leads outside the session's working directory. It shares
+	// CodeInvalidParams; the controller's /doc/file proxy maps it to 403, the
+	// status the local route answers.
+	ErrorPathOutsideSession ErrorInfo = "pathOutsideSession"
 	// ErrorKeybindingsPostRename marks a keybindings patch that APPLIED (the
 	// rename published the new revision) before a follow-up durable step
 	// failed; the error's data carries the applied canonical state.
@@ -552,6 +557,14 @@ func TranscriptItemCursorStale() WireError {
 	}
 }
 
+func PathOutsideSession(message string) WireError {
+	return WireError{
+		Code:    CodeInvalidParams,
+		Message: message,
+		Data:    ErrorData{EvenerErrorInfo: ErrorPathOutsideSession},
+	}
+}
+
 func ResourceNotFound(message string) WireError {
 	return WireError{
 		Code:    CodeInvalidParams,
```

`appwire/protocol.go`:

```diff
diff --git a/appwire/protocol.go b/appwire/protocol.go
--- a/appwire/protocol.go
+++ b/appwire/protocol.go
@@ -274,6 +274,7 @@ var Methods = []MethodSpec{
 	{MethodEvenerHostRunning, HostRunningParams{}, HostRunningResponse{}, ScopeHub, "Serves one hub's own running build revision and authoritative health to the controller probing it over an attached session, presenting the caller's required fencing epoch: process start time is present exactly when the hub knows it, and healthy reflects the local restart-required predicate, the owner-set minimum-free-space knob, and the state-root write probe."},
 	{MethodEvenerHostPushCredentials, HostPushCredentialsParams{}, HostPushCredentialsResponse{}, ScopeHub, "Copies the controller's local provider-instance keys to one named remote host (component 07c): each local store key is joined to the host's own instance by name (the lookup folds case), and the HOST's own spelling of the matched entry is what travels as Provider to evener/auth/status and evener/auth/apiKey/conditionalSet, the host classifies and writes its own store, and each entry reports added/updated/skipped/failed."},
 	{MethodEvenerSessionImage, SessionImageParams{}, SessionImageResponse{}, ScopeHub, "Fetches one image out of the recipient hub's own local session state for the controller's host-qualified image routes (component 05): SHA addresses a replayed transcript image and Path a session-relative file inside the session's working directory; the sha branch enforces the 8 MiB bound while scanning, and the media type is re-derived from the bytes. Never an HTTP route."},
+	{MethodEvenerSessionDocument, SessionDocumentParams{}, SessionDocumentResponse{}, ScopeHub, "Reads one document out of the recipient hub's own local session state for the controller's host-qualified /doc/file proxy (S7): the path is resolved inside the session's working directory by the local route's rule and refused as pathOutsideSession when it leads out; the answer is the file's first 512 KiB, its true size, the sha256 of the whole file (up to 16 MiB) and its modification time. Never an HTTP route."},
 }
 
 // ValidateMutationParams enforces the flag-day v2 identity and precondition
```

- [ ] **Step 4: Serve the method**

`cmd/evener-hub/doc_serve.go`:

```diff
diff --git a/cmd/evener-hub/doc_serve.go b/cmd/evener-hub/doc_serve.go
--- a/cmd/evener-hub/doc_serve.go
+++ b/cmd/evener-hub/doc_serve.go
@@ -93,6 +93,39 @@ func (s *WebServer) handleDocFile(w http.ResponseWriter, r *http.Request) {
 	writeDocFileRaw(w, r, doc)
 }
 
+// sessionDocumentFromHub serves the evener/session/document AppWire method: one
+// document out of THIS hub's own local session state, for the controller's
+// /doc/file proxy (S7). It is the AppWire counterpart of handleDocFile and
+// resolves the path by the same rule (sessionCWD, which refuses a session id
+// naming another source, then fspaths.ResolveInRoot), so a remote read is
+// confined to the session's folder exactly as a local one is.
+func sessionDocumentFromHub(cfg hubcore.WebConfig, params appwire.SessionDocumentParams) (appwire.SessionDocumentResponse, error) {
+	if params.SessionID == "" || params.Path == "" {
+		return appwire.SessionDocumentResponse{}, appwire.InvalidParams("sessionId and path are required")
+	}
+	cwd, ok := sessionCWD(cfg, canonicalRouteID(params.SessionID))
+	if !ok {
+		return appwire.SessionDocumentResponse{}, appwire.ResourceNotFound("session not found")
+	}
+	abs, err := fspaths.ResolveInRoot(cwd, params.Path)
+	if errors.Is(err, fspaths.ErrPathEscapesRoot) {
+		return appwire.SessionDocumentResponse{}, appwire.PathOutsideSession("path must resolve inside the session's working directory")
+	}
+	if err != nil {
+		return appwire.SessionDocumentResponse{}, appwire.ResourceNotFound("document not found")
+	}
+	doc, err := readDocFile(cwd, abs)
+	if err != nil {
+		return appwire.SessionDocumentResponse{}, appwire.ResourceNotFound("document not found")
+	}
+	return appwire.SessionDocumentResponse{
+		Data:       doc.Data,
+		TotalSize:  doc.TotalSize,
+		Revision:   doc.Revision,
+		ModifiedAt: docModifiedMillis(doc.ModifiedAt),
+	}, nil
+}
+
 // handleDocImage serves a validated image file inside a session's working
 // directory. It mirrors /doc/file's containment boundary, but only streams v1
 // supported image media types for inline output-image previews. A
```

`cmd/evener-hub/app_rpc.go`:

```diff
diff --git a/cmd/evener-hub/app_rpc.go b/cmd/evener-hub/app_rpc.go
--- a/cmd/evener-hub/app_rpc.go
+++ b/cmd/evener-hub/app_rpc.go
@@ -1291,6 +1291,11 @@ func registerThreadHandlers(
 	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSessionImage, func(_ context.Context, params appwire.SessionImageParams) (appwire.SessionImageResponse, error) {
 		return sessionImageFromHub(cfg, params)
 	})
+	// evener/session/document is the AppWire counterpart of the raw /doc/file
+	// read, for the controller's /doc/file proxy (S7).
+	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSessionDocument, func(_ context.Context, params appwire.SessionDocumentParams) (appwire.SessionDocumentResponse, error) {
+		return sessionDocumentFromHub(cfg, params)
+	})
 	appserver.HandleTyped(server.Router(), appwire.MethodThreadList, func(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
 		return hubThreadList(ctx, cfg, sources, params)
 	})
```

- [ ] **Step 5: Run the tests**

Run: `go test ./cmd/evener-hub -run 'TestHubSessionDocument' -count=1`
Expected: PASS.

- [ ] **Step 6: Decide the method in the catalog tests, and regenerate**

Run: `go test ./cmd/evener-hub -run 'TestHubRPCRegistersExpectedHandlerSet|TestHostAdminAllowListMatchesCatalog' -count=1`
Expected: FAIL, `catalog method "evener/session/document" has no allow/deny decision in this table` and `registered but NOT named: [evener/session/document]`.

`cmd/evener-hub/app_host_admin_test.go`:

```diff
diff --git a/cmd/evener-hub/app_host_admin_test.go b/cmd/evener-hub/app_host_admin_test.go
--- a/cmd/evener-hub/app_host_admin_test.go
+++ b/cmd/evener-hub/app_host_admin_test.go
@@ -396,10 +396,10 @@ func TestHostAdminAllowListMatchesCatalog(t *testing.T) {
 	// Rows are added one method at a time: a catalog method with no row fails the
 	// coverage check below, so a future addition still forces a decision.
 	//
-	// The session image fetch is denied for the same reason as the proxy method:
-	// it is the controller's own image-route call, resolving against the
-	// recipient's local session state, so a peer hub must not be able to drive
-	// it by forwarding the request.
+	// The session image and document fetches are denied for the same reason as
+	// the proxy method: each is the controller's own route call, resolving
+	// against the recipient's local session state, so a peer hub must not be
+	// able to drive it by forwarding the request.
 	policy := map[string]bool{
 		// The pulse meter read is not an admin RPC: the controller's own
 		// evener/activity/read already asks each attached host for its
@@ -531,6 +531,7 @@ func TestHostAdminAllowListMatchesCatalog(t *testing.T) {
 		"evener/session-pin/assign":               false,
 		"evener/session-pin/unpin":                false,
 		"evener/session/delete":                   false,
+		"evener/session/document":                 false,
 		"evener/session/image":                    false,
 		"evener/session/seen/set":                 false, // controller-owned: every source's seen marks live in the controller's own store
 		"evener/settings/agentsDoc/get":           true,
```

`cmd/evener-hub/app_rpc_test.go`:

```diff
diff --git a/cmd/evener-hub/app_rpc_test.go b/cmd/evener-hub/app_rpc_test.go
--- a/cmd/evener-hub/app_rpc_test.go
+++ b/cmd/evener-hub/app_rpc_test.go
@@ -12688,6 +12688,7 @@ func TestHubRPCRegistersExpectedHandlerSet(t *testing.T) {
 		appwire.MethodThreadList,
 		appwire.MethodThreadRead,
 		appwire.MethodEvenerSessionImage,
+		appwire.MethodEvenerSessionDocument,
 		appwire.MethodThreadUnsubscribe,
 		appwire.MethodThreadTurnsList,
 		appwire.MethodEvenerSubagentPreview,
```

Run: `make generate && go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1 && go test ./appwire -count=1 && go test ./cmd/evener-hub -run 'TestHubRPCRegistersExpectedHandlerSet|TestHostAdmin|TestHubRouterMatchesCatalog|TestHubSessionDocument|TestDocFile' -count=1`
Expected: PASS. `types.gen.ts` gains `SessionDocumentParams`, `SessionDocumentResponse` and the method's `METHOD_NAMES` and `MethodTypes` entries; `docs/appwire-protocol.md` gains the method row and the two types.

Run: `go vet ./appwire/ ./cmd/evener-hub/ && GOOS=windows go vet -tags evenerfuzz ./appwire/ ./cmd/evener-hub/ && golangci-lint run ./appwire/ ./cmd/evener-hub/ && $(go env GOROOT)/bin/gofmt -l appwire cmd/evener-hub`
Expected: clean. (gofmt realigns the `ErrorInfo` const block if the new constant is put inside its aligned run; it goes after `ErrorUpgradeRequired`, whose line has its own comment, so nothing moves.)

- [ ] **Step 7: Commit**

```bash
git add appwire/types.go appwire/errors.go appwire/protocol.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md \
  cmd/evener-hub/doc_serve.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_host_admin_test.go cmd/evener-hub/app_rpc_test.go \
  cmd/evener-hub/app_rpc_session_document_test.go
git commit -m "feat(hub): evener/session/document reads a session document on its own hub (S7, phase 7 PR 35)"
```

### Task 35.2: the controller's /doc/file reads a remote session through its host

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/appsource/remote_hub_source.go` (`FetchSessionDocument` after `FetchSessionImage`)
- Modify: `cmd/evener-hub/image_proxy.go` (`remoteSessionImageBudget` → `remoteSessionFileBudget`; `sessionImageFetcher` → `owningSourceAs[T]`; `hostQualifiedImageRef` → `hostQualifiedRouteRef`)
- Modify: `cmd/evener-hub/image_serve.go` (the renamed call)
- Modify: `cmd/evener-hub/doc_serve.go` (`handleDocFile`'s comment and remote branch; the renamed call in `handleDocImage`)
- Create: `cmd/evener-hub/doc_proxy.go`
- Modify: `cmd/evener-hub/doc_serve_test.go` (the two tests that pinned "a remote id is 404")
- Create: `cmd/evener-hub/session_document_route_test.go`

**Interfaces:**
- Consumes: `appwire.MethodEvenerSessionDocument`, `SessionDocumentParams`, `SessionDocumentResponse`, `ErrorPathOutsideSession` (Task 35.1); `docFileRead`, `writeDocFileRaw`, `docFileMaxBytes` (Task 34.1); `imageSha`, `imageShaRegexp` (`image_serve.go`); `wireErrorFromError`, `evenerErrorInfoFromData`, `statusForWireError` (`web_api.go`); test helpers `newScriptedRemoteHub`, `scriptedRemoteHubReplying`, `scriptedRemoteHubParams` (`remote_hub_source_rpc_test.go`) and those of Tasks 34.1 and 35.1.
- Produces:
  - `func (s *RemoteHubSource) FetchSessionDocument(ctx context.Context, params appwire.SessionDocumentParams) (appwire.SessionDocumentResponse, error)`
  - `func owningSourceAs[T any](sources *appsource.Registry, ref appwire.Ref) (T, bool)`, `func hostQualifiedRouteRef(id string) (appwire.Ref, bool)`, `const remoteSessionFileBudget`
  - `func (s *WebServer) serveRemoteSessionDocument(w, r, ref appwire.Ref, rel string)`, `sessionDocumentProxyStatus(err error) int`, `proxyableSessionDocument(resp) bool`
  - `/doc/file` statuses for a host-qualified id: 200 or 304, 400 (no `format=raw`, or invalid params from the host), 403, 404, 501, 502, 503.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/session_document_route_test.go`:

```go
package hub

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// controllerOverHost serves a controller hub with one attached host "h1" whose
// channel is client.
func controllerOverHost(t *testing.T, client *appwire.Client) *httptest.Server {
	t.Helper()
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostClientIfAttached(func(host string) (*appwire.Client, bool) {
		return client, host == "h1"
	})
	srv, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{})
	t.Cleanup(srv.Close)
	web.sources.Add(source)
	return srv
}

// controllerOverScriptedHost serves a controller whose host answers every
// evener/session/document with reply: a response, or an appwire.WireError.
func controllerOverScriptedHost(t *testing.T, reply any) (*httptest.Server, func() []appwire.SessionDocumentParams) {
	t.Helper()
	client, calls := newScriptedRemoteHub(t, scriptedRemoteHubReplying(appwire.MethodEvenerSessionDocument, reply))
	seen := func() []appwire.SessionDocumentParams {
		return scriptedRemoteHubParams[appwire.SessionDocumentParams](t, calls(), appwire.MethodEvenerSessionDocument)
	}
	return controllerOverHost(t, client), seen
}

func getRemoteDoc(t *testing.T, srv *httptest.Server, path string, header http.Header) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/doc/file?format=raw&session=h1%3A"+sessionImageTestSession+"&path="+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	maps.Copy(req.Header, header)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

// A session on another host reads through the controller's /doc/file exactly as
// a local one does: the real host hub reads its own session folder, and the
// controller answers with the local route's body and headers, revision and
// modification time included, and revalidates with 304.
func TestDocFileRouteReadsARemoteSessionThroughItsHost(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("# Plan\n\nStep one.\n")
	modified := time.UnixMilli(1_790_000_000_123)
	writeDocAt(t, filepath.Join(cwd, "plans", "plan.md"), content, modified)
	outside := t.TempDir()
	writeDocAt(t, filepath.Join(outside, "secret.txt"), []byte("secret"), modified)
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(cwd, "link.txt")); err != nil {
		t.Fatal(err)
	}
	host := sessionDocumentServer(t, cwd)
	client := dialHubRPC(t, host)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	controller := controllerOverHost(t, client)

	resp, body := getRemoteDoc(t, controller, "plans%2Fplan.md", nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("status=%d body=%q, want 200 and the host's file", resp.StatusCode, body)
	}
	etag := `"` + docRevisionOf(content) + `"`
	if resp.Header.Get("ETag") != etag || resp.Header.Get("X-Doc-Modified-At") != strconv.FormatInt(modified.UnixMilli(), 10) {
		t.Fatalf("ETag=%q X-Doc-Modified-At=%q, want %s and %d", resp.Header.Get("ETag"), resp.Header.Get("X-Doc-Modified-At"), etag, modified.UnixMilli())
	}
	if resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" || resp.Header.Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("Content-Type=%q Cache-Control=%q, want the local route's", resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
	}

	revalidated, body := getRemoteDoc(t, controller, "plans%2Fplan.md", http.Header{"If-None-Match": {etag}})
	if revalidated.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Fatalf("revalidation status=%d body=%q, want 304 and empty", revalidated.StatusCode, body)
	}

	// The host confines the read to the session's folder: a symlink leading
	// out and a path climbing out are 403, as on the local route.
	for _, path := range []string{"link.txt", "..%2F" + filepath.Base(outside) + "%2Fsecret.txt"} {
		if refused, body := getRemoteDoc(t, controller, path, nil); refused.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: status=%d body=%q, want 403", path, refused.StatusCode, body)
		}
	}
}

// The request names the session in the host's own namespace, and the path is
// forwarded as it came, for the host to resolve.
func TestDocFileRouteForwardsTheHostsOwnSessionID(t *testing.T) {
	srv, seen := controllerOverScriptedHost(t, appwire.SessionDocumentResponse{Data: []byte("hi"), TotalSize: 2, Revision: docRevisionOf([]byte("hi"))})
	if resp, _ := getRemoteDoc(t, srv, "dir%2Fnotes.txt", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	calls := seen()
	if len(calls) != 1 || calls[0].SessionID != sessionImageTestSession || calls[0].Path != "dir/notes.txt" {
		t.Fatalf("host calls = %+v, want one read of dir/notes.txt in session %s", calls, sessionImageTestSession)
	}
}

// A truncated read keeps the local route's truncation headers, and binary
// content is classified by the controller from the bytes themselves.
func TestDocFileRouteKeepsTruncationAndClassification(t *testing.T) {
	head := bytes.Repeat([]byte("a"), docFileMaxBytes)
	srv, _ := controllerOverScriptedHost(t, appwire.SessionDocumentResponse{Data: head, TotalSize: docFileMaxBytes + 100, Revision: docRevisionOf([]byte("the whole file"))})
	resp, body := getRemoteDoc(t, srv, "big.log", nil)
	if resp.StatusCode != http.StatusOK || len(body) != docFileMaxBytes {
		t.Fatalf("status=%d len=%d, want 200 and the head", resp.StatusCode, len(body))
	}
	if resp.Header.Get("X-Doc-Truncated") != "true" || resp.Header.Get("X-Doc-Total-Size") != strconv.Itoa(docFileMaxBytes+100) {
		t.Fatalf("truncation headers %q/%q", resp.Header.Get("X-Doc-Truncated"), resp.Header.Get("X-Doc-Total-Size"))
	}

	blob := []byte{0x00, 0x01, 0x02}
	srv, _ = controllerOverScriptedHost(t, appwire.SessionDocumentResponse{Data: blob, TotalSize: 3, Revision: docRevisionOf(blob)})
	if resp, _ := getRemoteDoc(t, srv, "blob.bin", nil); resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("Content-Type=%q, want application/octet-stream", resp.Header.Get("Content-Type"))
	}
}

// The host's refusals keep their meaning at the browser, and nothing falls
// back to a local read. A host built before S7 has no such method: 501, the
// phone's cue to keep its "Open it on the host" notice.
func TestDocFileRouteMapsHostRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply appwire.WireError
		want  int
	}{
		{"outside the session", appwire.PathOutsideSession("path must resolve inside the session's working directory"), http.StatusForbidden},
		{"not found", appwire.ResourceNotFound("document not found"), http.StatusNotFound},
		{"invalid params", appwire.InvalidParams("sessionId and path are required"), http.StatusBadRequest},
		{"host predates S7", appwire.WireError{Code: appwire.CodeMethodNotFound, Message: "method not found"}, http.StatusNotImplemented},
		{"host unavailable", appwire.SessionUnavailable("host detached"), http.StatusServiceUnavailable},
		{"host internal error", appwire.WireError{Code: appwire.CodeInternalError, Message: "boom"}, http.StatusServiceUnavailable},
		{"host conflict", appwire.WireError{Code: appwire.CodeConflict, Message: "busy"}, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := controllerOverScriptedHost(t, tc.reply)
			if resp, body := getRemoteDoc(t, srv, "notes.txt", nil); resp.StatusCode != tc.want {
				t.Fatalf("status=%d body=%q, want %d", resp.StatusCode, body, tc.want)
			}
		})
	}
}

// A host is bound to answer with at most the cap, a size that covers the bytes,
// a whole head when it truncates, a revision the bytes carry when they are the
// whole file, and a revision exactly when the file is small enough to hash. Anything else is refused as a bad gateway, not served.
func TestDocFileRouteRefusesAMalformedHostAnswer(t *testing.T) {
	hi := []byte("hi")
	for _, tc := range []struct {
		name  string
		reply appwire.SessionDocumentResponse
	}{
		{"over the cap", appwire.SessionDocumentResponse{Data: bytes.Repeat([]byte("a"), docFileMaxBytes+1), TotalSize: docFileMaxBytes + 1}},
		{"size smaller than the bytes", appwire.SessionDocumentResponse{Data: hi, TotalSize: 1}},
		{"a short head on a truncated read", appwire.SessionDocumentResponse{Data: hi, TotalSize: 10}},
		{"a revision that is not a sha256", appwire.SessionDocumentResponse{Data: hi, TotalSize: 2, Revision: "abc"}},
		{"a revision the whole file does not carry", appwire.SessionDocumentResponse{Data: hi, TotalSize: 2, Revision: docRevisionOf([]byte("ho"))}},
		{"no revision on a file small enough to hash", appwire.SessionDocumentResponse{Data: hi, TotalSize: 2}},
		{"a revision on a file too large to hash", appwire.SessionDocumentResponse{Data: bytes.Repeat([]byte("a"), docFileMaxBytes), TotalSize: docRevisionMaxBytes + 1, Revision: docRevisionOf(hi)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := controllerOverScriptedHost(t, tc.reply)
			if resp, body := getRemoteDoc(t, srv, "notes.txt", nil); resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status=%d (%d-byte body), want 502", resp.StatusCode, len(body))
			}
		})
	}
}

// A host-qualified request still needs format=raw, checked before the host is
// asked, and a host that is not attached is refused without a dial.
func TestDocFileRouteRefusesBeforeAskingTheHost(t *testing.T) {
	srv, seen := controllerOverScriptedHost(t, appwire.SessionDocumentResponse{})
	resp, err := srv.Client().Get(srv.URL + "/doc/file?session=h1%3At1&path=notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || len(seen()) != 0 {
		t.Fatalf("no format: status=%d host calls=%d, want 400 and none", resp.StatusCode, len(seen()))
	}

	var dials int
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		dials++
		return nil, appwire.SessionUnavailable("the attached-only path must never dial")
	})
	source.SetHostClientIfAttached(func(string) (*appwire.Client, bool) { return nil, false })
	detached, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{})
	t.Cleanup(detached.Close)
	web.sources.Add(source)
	if resp, _ := getRemoteDoc(t, detached, "notes.txt", nil); resp.StatusCode != http.StatusServiceUnavailable || dials != 0 {
		t.Fatalf("detached host: status=%d dials=%d, want 503 and no dial", resp.StatusCode, dials)
	}
}
```

Two existing tests pinned the old refusal of every remote id with a 404. A host-qualified id is now proxied, so without a registered source it is 503 (as `/doc/image` answers), and a non-raw request is refused before any host is asked:

`cmd/evener-hub/doc_serve_test.go`:

```diff
diff --git a/cmd/evener-hub/doc_serve_test.go b/cmd/evener-hub/doc_serve_test.go
--- a/cmd/evener-hub/doc_serve_test.go
+++ b/cmd/evener-hub/doc_serve_test.go
@@ -272,12 +272,13 @@ func TestDocFile_UnknownSession404(t *testing.T) {
 	}
 }
 
-func TestDocFile_NonLocalSession404(t *testing.T) {
+func TestDocFile_NonLocalSessionNeedsRawFormat400(t *testing.T) {
 	web, _, _ := docServeTestServer(t)
-	// A non-local remote ref must be skipped — local sources only.
+	// A host-qualified ref is never read locally, and a request the route
+	// would refuse is refused before any host is asked.
 	rec := docRequest(t, web, "remote:th_remote", "README.md")
-	if rec.Code != http.StatusNotFound {
-		t.Errorf("non-local session should 404, got %d body=%q", rec.Code, rec.Body.String())
+	if rec.Code != http.StatusBadRequest {
+		t.Errorf("non-raw request for a non-local session should 400, got %d body=%q", rec.Code, rec.Body.String())
 	}
 }
 
@@ -409,11 +410,13 @@ func TestDocFile_Raw_UnknownSession404(t *testing.T) {
 	}
 }
 
-func TestDocFile_Raw_NonLocalSession404(t *testing.T) {
+func TestDocFile_Raw_UnknownHostSession503(t *testing.T) {
 	web, _, _ := docServeTestServer(t)
+	// No source named "remote" is registered: the read is refused as the
+	// host being unavailable, never resolved against this hub's own disk.
 	rec := docRawRequest(t, web, "remote:th_remote", "README.md")
-	if rec.Code != http.StatusNotFound {
-		t.Errorf("non-local session should 404, got %d body=%q", rec.Code, rec.Body.String())
+	if rec.Code != http.StatusServiceUnavailable {
+		t.Errorf("a session on an unknown host should 503, got %d body=%q", rec.Code, rec.Body.String())
 	}
 }
 
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./cmd/evener-hub -run 'TestDocFileRoute|TestDocFile_NonLocal|TestDocFile_Raw_UnknownHost' -count=1`
Expected: FAIL. Every `TestDocFileRoute*` case gets 404 (`404 page not found`) where it wants 200, 304, 403, 501, 502 or 503; `TestDocFile_NonLocalSessionNeedsRawFormat400` gets 404; `TestDocFile_Raw_UnknownHostSession503` gets 404.

- [ ] **Step 3: The host source reads a document**

`cmd/evener-hub/internal/appsource/remote_hub_source.go`:

```diff
diff --git a/cmd/evener-hub/internal/appsource/remote_hub_source.go b/cmd/evener-hub/internal/appsource/remote_hub_source.go
--- a/cmd/evener-hub/internal/appsource/remote_hub_source.go
+++ b/cmd/evener-hub/internal/appsource/remote_hub_source.go
@@ -601,6 +601,17 @@ func (s *RemoteHubSource) FetchSessionImage(ctx context.Context, params appwire.
 	return out, nil
 }
 
+// FetchSessionDocument reads one document out of the owning host's own session
+// folder, for the controller's /doc/file proxy (S7). Like FetchSessionImage it
+// is attached-only and needs no translation: it carries bytes, not refs.
+func (s *RemoteHubSource) FetchSessionDocument(ctx context.Context, params appwire.SessionDocumentParams) (appwire.SessionDocumentResponse, error) {
+	var out appwire.SessionDocumentResponse
+	if err := s.call(ctx, appwire.MethodEvenerSessionDocument, params, &out); err != nil {
+		return appwire.SessionDocumentResponse{}, err
+	}
+	return out, nil
+}
+
 // ReadSessionActivity reads the host's own live sessions' pulse meters for the
 // controller's evener/activity/read (S5). Refs are rewritten into the host's
 // namespace on the way out and back into the controller's on the way in; a
```

- [ ] **Step 4: Share the image proxy's source lookup, id check and budget**

`cmd/evener-hub/image_proxy.go`:

```diff
diff --git a/cmd/evener-hub/image_proxy.go b/cmd/evener-hub/image_proxy.go
--- a/cmd/evener-hub/image_proxy.go
+++ b/cmd/evener-hub/image_proxy.go
@@ -9,10 +9,10 @@ import (
 	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
 )
 
-// remoteSessionImageBudget bounds one proxied image read. The host reads a
-// bounded amount from its own disk, so anything longer is a stalled channel,
-// not a slow read; the request's own context still cancels earlier.
-const remoteSessionImageBudget = 30 * time.Second
+// remoteSessionFileBudget bounds one proxied image or document read. The host
+// reads a bounded amount from its own disk, so anything longer is a stalled
+// channel, not a slow read; the request's own context still cancels earlier.
+const remoteSessionFileBudget = 30 * time.Second
 
 // remoteSessionImageFetcher is the source capability the two image routes use to
 // serve a session that lives on another host. A source that cannot fetch (this
@@ -22,17 +22,18 @@ type remoteSessionImageFetcher interface {
 	FetchSessionImage(ctx context.Context, params appwire.SessionImageParams) (appwire.SessionImageResponse, error)
 }
 
-// sessionImageFetcher resolves the source a host-qualified route id names to the
-// capability the image routes call. A missing registry, an unregistered source,
-// and a source with no fetch capability are one refusal: these routes can only
-// be served from the owning host, never from a local read.
-func sessionImageFetcher(sources *appsource.Registry, ref appwire.Ref) (remoteSessionImageFetcher, bool) {
+// owningSourceAs resolves the source a host-qualified route id names to the
+// capability T a proxied route calls. A missing registry, an unregistered
+// source, and a source without T are one refusal: these routes can only be
+// served from the owning host, never from a local read.
+func owningSourceAs[T any](sources *appsource.Registry, ref appwire.Ref) (T, bool) {
+	var none T
 	source, err := sourceForThread(sources, ref.String(), "")
 	if err != nil {
-		return nil, false
+		return none, false
 	}
-	fetcher, ok := source.(remoteSessionImageFetcher)
-	return fetcher, ok
+	capability, ok := source.(T)
+	return capability, ok
 }
 
 // proxyableSessionImage reports whether a host's answer is one of the shapes the
@@ -58,11 +59,12 @@ func proxyableSessionImage(resp appwire.SessionImageResponse, wantSHA string) bo
 	return ok && mediaType == resp.MediaType
 }
 
-// hostQualifiedImageRef reports whether a route id is the host-qualified form
+// hostQualifiedRouteRef reports whether a route id is the host-qualified form
 // `s.id + ":" + <remote session id>` the outbound image translation writes for a
-// remote session. A bare (legacy local) id and a "local:" id are not
-// host-qualified and resolve against this hub's own state exactly as before.
-func hostQualifiedImageRef(id string) (appwire.Ref, bool) {
+// remote session, and a remote session's thread ref has. A bare (legacy local)
+// id and a "local:" id are not host-qualified and resolve against this hub's
+// own state exactly as before.
+func hostQualifiedRouteRef(id string) (appwire.Ref, bool) {
 	ref, err := appwire.ParseRef(id)
 	if err != nil || ref.SourceID == "local" {
 		return appwire.Ref{}, false
@@ -96,12 +98,12 @@ func (s *WebServer) serveRemoteSessionImage(w http.ResponseWriter, r *http.Reque
 		http.Error(w, "GET required", http.StatusMethodNotAllowed)
 		return
 	}
-	fetcher, ok := sessionImageFetcher(s.sources, ref)
+	fetcher, ok := owningSourceAs[remoteSessionImageFetcher](s.sources, ref)
 	if !ok {
 		http.Error(w, "remote host unavailable", http.StatusServiceUnavailable)
 		return
 	}
-	ctx, cancel := context.WithTimeout(r.Context(), remoteSessionImageBudget)
+	ctx, cancel := context.WithTimeout(r.Context(), remoteSessionFileBudget)
 	defer cancel()
 	resp, err := fetcher.FetchSessionImage(ctx, params)
 	if err != nil {
```

`cmd/evener-hub/image_serve.go`:

```diff
diff --git a/cmd/evener-hub/image_serve.go b/cmd/evener-hub/image_serve.go
--- a/cmd/evener-hub/image_serve.go
+++ b/cmd/evener-hub/image_serve.go
@@ -42,7 +42,7 @@ func (s *WebServer) handleSessionImage(w http.ResponseWriter, r *http.Request, s
 	// on that host's filesystem, so the request is proxied to the owning source
 	// instead of resolved here. A bare or "local:" id resolves exactly as
 	// before.
-	if ref, ok := hostQualifiedImageRef(sessionID); ok {
+	if ref, ok := hostQualifiedRouteRef(sessionID); ok {
 		s.serveRemoteSessionImage(w, r, ref, appwire.SessionImageParams{SessionID: ref.ThreadID, SHA: sha})
 		return
 	}
```

- [ ] **Step 5: Proxy /doc/file for a host-qualified session**

`cmd/evener-hub/doc_proxy.go`:

```go
package hub

import (
	"context"
	"net/http"
	"time"

	"primeradiant.com/evener/appwire"
)

// remoteSessionDocumentFetcher is the source capability /doc/file uses to read a
// file in a session that lives on another host (S7).
type remoteSessionDocumentFetcher interface {
	FetchSessionDocument(ctx context.Context, params appwire.SessionDocumentParams) (appwire.SessionDocumentResponse, error)
}

// serveRemoteSessionDocument reads one file of a session that lives on another
// host: the owning host resolves the path inside its own session folder
// (sessionDocumentFromHub), and this hub answers with the local route's shapes
// (writeDocFileRaw). Every failure is a refusal, never a local read.
func (s *WebServer) serveRemoteSessionDocument(w http.ResponseWriter, r *http.Request, ref appwire.Ref, rel string) {
	fetcher, ok := owningSourceAs[remoteSessionDocumentFetcher](s.sources, ref)
	if !ok {
		http.Error(w, "remote host unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), remoteSessionFileBudget)
	defer cancel()
	resp, err := fetcher.FetchSessionDocument(ctx, appwire.SessionDocumentParams{SessionID: ref.ThreadID, Path: rel})
	if err != nil {
		http.Error(w, "remote document unavailable", sessionDocumentProxyStatus(err))
		return
	}
	if !proxyableSessionDocument(resp) {
		http.Error(w, "remote document malformed", http.StatusBadGateway)
		return
	}
	doc := docFileRead{Data: resp.Data, TotalSize: resp.TotalSize, Revision: resp.Revision}
	if resp.ModifiedAt > 0 {
		doc.ModifiedAt = time.UnixMilli(resp.ModifiedAt)
	}
	writeDocFileRaw(w, r, doc)
}

// sessionDocumentProxyStatus maps one host AppWire failure onto the status the
// local route answers for the same case: a path outside the session is 403, a
// missing session or file is 404, and a malformed request is 400. A host built
// before S7 answers MethodNotFound, which is 501 so a client can tell "this
// host can't serve documents yet" from "this file is gone". Every other
// failure (an unattached or unknown host, a transport failure, the host's own
// internal error or conflict) is 503: the document is unavailable right now.
func sessionDocumentProxyStatus(err error) int {
	wire, ok := wireErrorFromError(err)
	if !ok {
		return http.StatusServiceUnavailable
	}
	switch appwire.ErrorInfo(evenerErrorInfoFromData(wire.Data)) {
	case appwire.ErrorPathOutsideSession:
		return http.StatusForbidden
	case appwire.ErrorResourceNotFound:
		return http.StatusNotFound
	}
	switch wire.Code {
	case appwire.CodeMethodNotFound:
		return http.StatusNotImplemented
	case appwire.CodeInvalidParams, appwire.CodeInvalidRequest:
		return http.StatusBadRequest
	default:
		return http.StatusServiceUnavailable
	}
}

// proxyableSessionDocument reports whether a host's answer is one the local
// route could have produced: at most docFileMaxBytes, a total size that covers
// them, the whole head when the file was truncated, a revision exactly when
// the file is small enough to hash, and, when the bytes are the whole file, the
// revision those bytes carry. The controller cannot check a truncated file's revision (it has
// only the head), so it checks the form.
func proxyableSessionDocument(resp appwire.SessionDocumentResponse) bool {
	n := int64(len(resp.Data))
	if n > docFileMaxBytes || resp.TotalSize < n {
		return false
	}
	truncated := resp.TotalSize > n
	if truncated && n != docFileMaxBytes {
		return false
	}
	// The host hashes every file up to docRevisionMaxBytes and no larger one.
	if resp.TotalSize > docRevisionMaxBytes {
		return resp.Revision == ""
	}
	if !imageShaRegexp.MatchString(resp.Revision) {
		return false
	}
	return truncated || resp.Revision == imageSha(resp.Data)
}
```

`cmd/evener-hub/doc_serve.go`:

```diff
diff --git a/cmd/evener-hub/doc_serve.go b/cmd/evener-hub/doc_serve.go
--- a/cmd/evener-hub/doc_serve.go
+++ b/cmd/evener-hub/doc_serve.go
@@ -35,10 +35,12 @@ const docFileMaxBytes = 512 * 1024
 // ~/git was 3.2 MB. A larger file is served without a revision.
 const docRevisionMaxBytes = 16 * 1024 * 1024
 
-// handleDocFile serves a LOCAL session file's literal bytes for the React
+// handleDocFile serves a session file's literal bytes for the React
 // doc-viewer pane, which renders the content itself. The route has a single
 // mode, ?format=raw; a request that omits format or sends any other value is a
-// client error (400 with a hint naming the parameter).
+// client error (400 with a hint naming the parameter). A host-qualified session
+// id names a session on another host, so its file is read by the owning host
+// (serveRemoteSessionDocument); a local id reads this hub's own filesystem.
 //
 // The guard chain below (session/path presence, cwd containment) runs before
 // the format check, so a raw and a non-raw request reject the same out-of-cwd
@@ -61,6 +63,16 @@ func (s *WebServer) handleDocFile(w http.ResponseWriter, r *http.Request) {
 		http.NotFound(w, r)
 		return
 	}
+	if ref, ok := hostQualifiedRouteRef(session); ok {
+		// Checked before the host is asked: a request this route would refuse
+		// is never forwarded.
+		if r.URL.Query().Get("format") != "raw" {
+			http.Error(w, "format=raw required", http.StatusBadRequest)
+			return
+		}
+		s.serveRemoteSessionDocument(w, r, ref, rel)
+		return
+	}
 
 	cwd, ok := s.localSessionCWD(session)
 	if !ok {
@@ -143,7 +155,7 @@ func (s *WebServer) handleDocImage(w http.ResponseWriter, r *http.Request) {
 		http.NotFound(w, r)
 		return
 	}
-	if ref, ok := hostQualifiedImageRef(session); ok {
+	if ref, ok := hostQualifiedRouteRef(session); ok {
 		s.serveRemoteSessionImage(w, r, ref, appwire.SessionImageParams{SessionID: ref.ThreadID, Path: rel})
 		return
 	}
```

- [ ] **Step 6: Run the tests, the image proxy's tests and the source's tests**

Run: `go test ./cmd/evener-hub -run 'TestDocFileRoute|TestDocFile|TestSessionImage|TestHubSession|TestCov' -count=1 && go test ./cmd/evener-hub -run '^(FuzzSmallFaultsPass5|FuzzCovThreadreadImagesSeed100)$' -count=1 && go test ./cmd/evener-hub/internal/appsource -count=1`
Expected: PASS.

Run: `go vet ./cmd/evener-hub/... && go vet -tags evenerfuzz ./cmd/evener-hub/... && GOOS=windows go vet -tags evenerfuzz ./cmd/evener-hub/... && golangci-lint run ./cmd/evener-hub/ ./cmd/evener-hub/internal/appsource/ && $(go env GOROOT)/bin/gofmt -l cmd/evener-hub`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add cmd/evener-hub/internal/appsource/remote_hub_source.go cmd/evener-hub/image_proxy.go cmd/evener-hub/image_serve.go \
  cmd/evener-hub/doc_serve.go cmd/evener-hub/doc_proxy.go cmd/evener-hub/doc_serve_test.go cmd/evener-hub/session_document_route_test.go
git commit -m "feat(hub): /doc/file reads a remote session's document through its host (S7, phase 7 PR 35)"
```

### Task 35.3: a host that predates S7 says "Open it on the host"

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire-client/typescript/docContent.ts` (`DocFileErrorKind` gains `"host-unsupported"`; `errorKindForStatus` maps 501; `DocFileError`'s comment)
- Modify: `appwire-client/typescript/docContent.test.ts` (one test before "any other non-ok status")
- Modify: `cmd/evener-hub/frontend/src/panes/doc/DocPane.tsx` (`ERROR_COPY` row and its comment)
- Modify: `cmd/evener-hub/frontend/src/panes/doc/DocPane.test.tsx` (one test before "any other failure")

**Interfaces:**
- Consumes: the proxy's 501 (Task 35.2).
- Produces: `DocFileErrorKind = "forbidden" | "not-found" | "host-unsupported" | "error"`. The phone lane maps `host-unsupported` to its `elsewhere` document (Phone lane handoff); until then the phone's `loadDocument` shows it as `failed`, and it never asks a remote session anyway.

- [ ] **Step 1: Write the failing tests**

`appwire-client/typescript/docContent.test.ts`:

```diff
diff --git a/appwire-client/typescript/docContent.test.ts b/appwire-client/typescript/docContent.test.ts
--- a/appwire-client/typescript/docContent.test.ts
+++ b/appwire-client/typescript/docContent.test.ts
@@ -218,6 +218,14 @@ describe("readDocFile", () => {
     await expect(readDocFile("s1", "gone.txt", port)).rejects.toMatchObject({ kind: "not-found", status: 404 });
   });
 
+  test("a 501 (the session's host predates S7 document reads) rejects with a host-unsupported DocFileError", async () => {
+    const port = respondWith(new Response("remote document unavailable", { status: 501 }));
+    await expect(readDocFile("h1:s1", "plan.md", port)).rejects.toMatchObject({
+      kind: "host-unsupported",
+      status: 501,
+    });
+  });
+
   test("any other non-ok status rejects with a generic error DocFileError carrying the status", async () => {
     const port = respondWith(new Response("boom", { status: 500 }));
     await expect(readDocFile("s1", "x.txt", port)).rejects.toMatchObject({ kind: "error", status: 500 });
```

`cmd/evener-hub/frontend/src/panes/doc/DocPane.test.tsx`:

```diff
diff --git a/cmd/evener-hub/frontend/src/panes/doc/DocPane.test.tsx b/cmd/evener-hub/frontend/src/panes/doc/DocPane.test.tsx
--- a/cmd/evener-hub/frontend/src/panes/doc/DocPane.test.tsx
+++ b/cmd/evener-hub/frontend/src/panes/doc/DocPane.test.tsx
@@ -135,6 +135,12 @@ test("a 403 (path escapes the cwd) shows the access-denied empty state", async (
   expect(await screen.findByText("Access denied")).toBeTruthy();
 });
 
+test("a 501 (the session's host can't serve documents yet) says to open it on the host", async () => {
+  mockRead.mockRejectedValue(new DocFileError("host-unsupported", 501));
+  renderFile("plan.md");
+  expect(await screen.findByText("Open it on the host")).toBeTruthy();
+});
+
 test("any other failure shows a generic couldn't-load empty state", async () => {
   mockRead.mockRejectedValue(new DocFileError("error", 500));
   renderFile("x.txt");
```

- [ ] **Step 2: Run them to see them fail**

Run (from `cmd/evener-hub/frontend`): `npx vitest run ../../../appwire-client/typescript/docContent.test.ts src/panes/doc/DocPane.test.tsx`
Expected: both new tests FAIL (`kind: "error"`; no "Open it on the host").

- [ ] **Step 3: Map 501, and give the web its copy**

`appwire-client/typescript/docContent.ts`:

```diff
diff --git a/appwire-client/typescript/docContent.ts b/appwire-client/typescript/docContent.ts
--- a/appwire-client/typescript/docContent.ts
+++ b/appwire-client/typescript/docContent.ts
@@ -40,12 +40,13 @@ export interface DocFileContent {
 // is no longer inferred from the body length.
 export const DOC_FILE_MAX_BYTES = 512 * 1024;
 
-export type DocFileErrorKind = "forbidden" | "not-found" | "error";
+export type DocFileErrorKind = "forbidden" | "not-found" | "host-unsupported" | "error";
 
 // A failed raw-file fetch, carrying the honest HTTP status so the pane maps it
 // to the same guard/status contract the HTML variant enforces: 403 for a path
-// that escapes the session cwd, 404 for a missing file / unknown or non-local
-// session, and a generic error for anything else (doc_serve.go:57-73).
+// that escapes the session cwd, 404 for a missing file or unknown session, 501
+// for a session on a host whose hub predates remote document reads (S7,
+// cmd/evener-hub/doc_proxy.go), and a generic error for anything else.
 export class DocFileError extends Error {
   readonly kind: DocFileErrorKind;
   readonly status: number;
@@ -87,6 +88,7 @@ export interface DocPort {
 function errorKindForStatus(status: number): DocFileErrorKind {
   if (status === 403) return "forbidden";
   if (status === 404) return "not-found";
+  if (status === 501) return "host-unsupported";
   return "error";
 }
 
```

`cmd/evener-hub/frontend/src/panes/doc/DocPane.tsx`:

```diff
diff --git a/cmd/evener-hub/frontend/src/panes/doc/DocPane.tsx b/cmd/evener-hub/frontend/src/panes/doc/DocPane.tsx
--- a/cmd/evener-hub/frontend/src/panes/doc/DocPane.tsx
+++ b/cmd/evener-hub/frontend/src/panes/doc/DocPane.tsx
@@ -27,12 +27,17 @@ const CLASS = {
 };
 
 // Error kind -> empty-state copy. The raw endpoint shares the HTML variant's
-// guard/status contract exactly (cmd/evener-hub/doc_serve.go:57-73): 403 for a
-// path that escapes the session cwd, 404 for a missing file or an unknown /
-// non-local session, and a generic error for anything else.
+// guard/status contract exactly (cmd/evener-hub/doc_serve.go): 403 for a path
+// that escapes the session cwd, 404 for a missing file or an unknown session,
+// 501 for a session whose host predates remote document reads
+// (cmd/evener-hub/doc_proxy.go), and a generic error for anything else.
 const ERROR_COPY: Record<DocFileErrorKind, { title: string; hint: string }> = {
   forbidden: { title: "Access denied", hint: "This path is outside the session's working directory." },
   "not-found": { title: "File not available", hint: "This file was not found in the session's working directory." },
+  "host-unsupported": {
+    title: "Open it on the host",
+    hint: "This session's host runs an older Evener that can't send its files here yet.",
+  },
   error: { title: "Couldn't load file", hint: "The hub returned an unexpected error." },
 };
 
```

- [ ] **Step 4: Run the tests, the typecheck, Biome, and the phone's typecheck**

Run (from `cmd/evener-hub/frontend`): `npx vitest run ../../../appwire-client/typescript/ src/panes/doc/ && npm run typecheck && npx biome check --write ../../../appwire-client/typescript/docContent.ts ../../../appwire-client/typescript/docContent.test.ts src/panes/doc/DocPane.tsx src/panes/doc/DocPane.test.tsx`
Expected: PASS, and Biome clean after its write (it wraps the long `toMatchObject` line).

Run (from `mobile-native`, with its own `npm ci` done if `node_modules` is missing and not a symlink): `npm run check && npx vitest run src/reader`
Expected: no type errors; the Reader's tests pass unchanged.

- [ ] **Step 5: Commit**

```bash
git add appwire-client/typescript/docContent.ts appwire-client/typescript/docContent.test.ts \
  cmd/evener-hub/frontend/src/panes/doc/DocPane.tsx cmd/evener-hub/frontend/src/panes/doc/DocPane.test.tsx
git commit -m "feat(web): a host that can't send documents yet says to open it there (S7, phase 7 PR 35)"
```

**PR.** Title "feat: documents in remote sessions (S7, phase 7 PR 35)". The body says images already proxy (#2462), how the read stays inside the session's folder (ruling 9, and the end-to-end test through a real host hub), the status mapping (ruling 11), the rename (ruling 13), and the phone lane's S7 switch-over.

---

## Self-review

- **Spec coverage.**
  - 10.2: "updated 3m ago" from the file's own time (`modifiedAt`, PR 34 and the handoff; question 2); "3 changes since you read it yesterday" keyed on the revision, with the block hashes still counting the changes (PR 34 and the handoff; question 1); the 512 KB note keeps its source, `X-Doc-Truncated` and `X-Doc-Total-Size`, on both routes.
  - 8.2: the document chip's "changed since you last read" can compare revisions (the handoff).
  - 18: S7 (images already done; documents in PR 35, fallback kept for an old host through `host-unsupported`) and S9 (PR 34, fallback kept for a file over 16 MiB and an older hub).
- **Checked by running.**
  - Every task was implemented on a local scratch branch from main at `947c50235` (never pushed), and the code blocks above are rendered from its commits.
  - These passed: `go build ./...`; `go vet` plain, with `evenerfuzz` and for Windows on `./cmd/evener-hub/...` and `./appwire/...`; `golangci-lint` 2.13.1 on `./cmd/evener-hub/`, `./appwire/` and `./cmd/evener-hub/internal/appsource/`; the whole `./cmd/evener-hub/...`, `./appwire/...` and `./internal/appwirets` suites; the two coverage fuzz seeds; `TestGeneratedFileCurrent`; the frontend typecheck and its `src` vitest suites; the package's vitest suite (2,676 tests); `mobile-native`'s `npm run check` and its Reader tests.
  - Each red step named above was run and failed as described.
  - The first dry run of Task 34.1 found the linter's `modernize` findings (`strings.SplitSeq`, `maps.Copy`); the code above carries the fix.
  - RoboRev's third round found that the read opened the checked path again by name, so a symlink swapped in after the check could lead it out; that the client took any quoted tag as a revision; and that the proxy passed a host's internal error or conflict through as 500 or 409. `readDocFile` now opens through `os.Root`, non-blocking; `etagRevision` takes only a strong sha256 tag; the status mapping names every code. Each fix came with a red-first test.
  - RoboRev's second round found that a file shrinking below 16 MiB after the stat went unhashed, and that the proxy accepted a missing revision on a hashable file. Both are fixed above with tests.
  - RoboRev on the plan PR found that the proxy accepted a revision on a file too large to hash, that `If-None-Match: *` was ignored for a file with no revision, and that the server sent pre-epoch times the client drops. The code above carries the fixes and their tests.
  - The plan PR's review found `writeDocAt`'s directory creation in the wrong task, a stale size for a file growing past the hash limit mid-read, and no test at the limit itself or for `If-None-Match: *`. The dry run was rebuilt with the fixes and every check above re-run.
- **Not run.** `make test-web-browser` and `make test-native` as a whole (nothing here renders on the phone), `make lint` as a whole (the pinned linter ran per package), a real two-machine ssh attach (the proxy test runs a real host hub over a WebSocket AppWire channel; the ssh transport carries the same frames), and anything on Linux.
- **Placeholders.** None: every task carries its code or the exact edit.
- **Names.** One spelling across tasks:
  - S9: `docFileRead` (`Data`, `TotalSize`, `Revision`, `ModifiedAt`), `readDocFile`, `writeDocFileRaw`, `ifNoneMatchNames`, `docModifiedMillis`, `docRevisionMaxBytes`; headers `ETag`, `X-Doc-Modified-At`; `DocFileContent.revision`, `.modifiedAt`, `etagRevision`.
  - S7: `MethodEvenerSessionDocument`, `SessionDocumentParams`, `SessionDocumentResponse` (`data`, `totalSize`, `revision`, `modifiedAt`), `ErrorPathOutsideSession`, `PathOutsideSession`, `sessionDocumentFromHub`, `FetchSessionDocument`, `remoteSessionDocumentFetcher`, `owningSourceAs`, `hostQualifiedRouteRef`, `remoteSessionFileBudget`, `serveRemoteSessionDocument`, `sessionDocumentProxyStatus`, `proxyableSessionDocument`; `DocFileErrorKind` `"host-unsupported"`.
- **Review Focus.** Each line names the tests that pin it, in the task that owns the code.
- **Departures from the server plan's S7 and S9 sketches.**
  - S7 is one PR: the image method and proxy it planned landed in #2462.
  - The method is `evener/session/document` with `{sessionId, path}`, beside the image method, rather than `evener/doc/read` with `{ref, path}`: the host is addressed by the source the controller picks, and the id is the host's own.
  - Both methods stay off the host admin proxy (ruling 12), where the sketch put them on it.
  - S9's revision is a whole-file sha256, not a weak tag from size, time and the head's hash (ruling 1), and the time is `X-Doc-Modified-At`, not `Last-Modified` (ruling 4).
  - `DocFetch` gains no request headers: nothing on the phone needs to send `If-None-Match` by hand (ruling 5).
