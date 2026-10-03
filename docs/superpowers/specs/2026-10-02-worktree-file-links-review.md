# Worktree filename links: adversarial design review

## Status and scope

Two independent reviewers returned **revise** on the initial spec at commit `ff6fbc6d417b313ee8b39c7a183038cf09b7da4b`. The revised spec incorporates the corrections below and awaits their focused second review. Jesse chose to keep a clicked secondary delegate conversation visible beside its file, retaining the prior parent. Implementation has not started. Written-spec approval and implementation-plan approval remain separate gates.

Design: [Clickable worktree filenames](2026-10-02-worktree-file-links-design.md).

Jesse approved assistant prose, inline-code paths and Markdown file links, including remote sessions, using the existing web document pane and native Reader. User messages, logs and parent-embedded delegate reports stay out of scope. The feature opens current files without recognition-time existence checks.

Main-checkout files remain untouched: `batch-notes.md`, `evener-fluency.exe`, and `notes/experience/isolation-missed-project-config.md`. Jesse's preservation instruction was “Leave them untouched.”

## Reviewers and original evidence

- Backend/parser/containment: `dlg_034ZC9kztW454YzzD7LTxs`, transcript `local:034ZC9kztW932wVXwkgLIT`, final report turn 132.
- Client/UX/lifecycle: `dlg_034ZCAJ0I3saVIrLOvOg1r`, transcript `local:034ZCAJ0I3wl7NBGrl0Lf4`, final report turn 165.

Both reviewed independently against the original spec. Original line numbers below refer to that committed version, not the revised working file. Neither changed product files or installed dependencies.

## Backend/parser findings and resolutions

| Finding | Evidence and counterexample | Resolution and required proof |
| --- | --- | --- |
| High: live cwd publication is absent, not briefly delayed, original lines 109–113/186 | `agent/session_env_swap.go:180–181` updates EnvInfo, but `server/appwire_runtime.go:2401,2466` reads launch status. `server/bridge.go:200–233` has no cwd effect. Hub `app_threadread.go:498–499` and `app_threadlist.go:437–438` fill only empty cwd. `doc_serve.go:226–243` also prefers cached past. | Publish successful environment changes through live thread cwd and source-client invalidation. Current live root must outrank cached past; archived metadata must refresh. Prove real enter/switch/exit, hydrate/reconnect and document bytes together, including stale Past=A/current=B and remote owning-host reads. |
| High: absolute-to-relative conversion silently retargets, original lines 102/109–113/123/145 | `docContent.ts:219–224` drops the absolute root, and `fileOpenBeside.tsx:40–42` keeps only a relative path. Client A/server B can serve B's same-named file. Focus-only pane dedup can retain A. | Separate normalized display/dedup path, cwd binding and absolute read target. Preserve absolute provenance. Rebind a retained pane explicitly and invalidate old publications. Relative viewers follow acknowledged current cwd as a new identity; absolute old targets stay unavailable. Prove delayed hydration/read across conflicting A/B files. |
| Medium: accepted lexical aliases disagree, original lines 102/123 | `docs/a.md`, `docs/./a.md` and `docs//a.md` currently remain distinct client identities while `fspaths/paths.go:93–96` collapses them. | Reject traversal before normalization, then collapse accepted dot/repeated-separator aliases. One pane/chip identity; literal percent filenames remain distinct. |
| Medium: removing remote preflight loses the 501 explanation, original lines 141/181 | Shared transport returns `host-unsupported`, while `mobile-native/src/reader/documentSource.ts:58–61` maps it to generic `failed`. | Add a distinct native unsupported-capability result from actual transport 501. Keep 503 retryable; prove supported remote transport and 403/404/501/503 outcomes. |
| Medium: URI decoding and suffix order are ambiguous, original lines 89–90/100–104 | Decoding `a.md%3A12` before stripping locations changes a literal colon filename into `a.md`. Bare link `README.md:12` is scheme-shaped; inline code has different semantics. | Check URI schemes and raw metadata/location suffixes before one percent decode. Strip literal numeric locations before literal validation. Define encoded colon/hash/question mark as filename data. Prove distinct real files return intended bytes. |
| Medium: formatting edges are mistaken for filename boundaries, original lines 92–96/117/133 | `../**docs/plan.md**` can yield a shorter forbidden suffix when nodes/tokens are scanned alone. | Validate across adjacent inline structure, with conservative treatment of split candidates. Test traversal/scheme prefixes, interior parentheses, curly quotes and terminal ellipsis. |
| Low: syntax cannot prove a candidate is a file, original lines 54/88–90/164 | A directory may be named `docs/archive.md`; recognition performs no reads. | Promise syntactic directory rejection, not filesystem existence/type detection. Actual directories use the non-regular-file failure state. |
| Absolute cwd-alias feasibility | `fspaths/paths.go:87–100` compares absolute spelling against resolved root, so trusted `alias→real` accepts relative/canonical targets but refuses `alias/plan.md`. | Normalize only the current trusted cwd prefix to its real root, retaining final symlink containment and rooted open. Prove relative/alias/canonical equivalence and reject siblings, old roots, escaping child symlinks and a child-symlink replacement before open. No generation protocol or arbitrary alias fallback. |

Absolute targets already pass through remote transport unchanged (`doc_proxy.go:29`) and are validated at the owning host (`doc_serve.go:125–129`). The revision grants no old/outside-worktree reads.

## Client/UX/lifecycle findings and resolutions

| Finding | Evidence and counterexample | Resolution and required proof |
| --- | --- | --- |
| High: cwd replacement is absent from read identity, original lines 109–113/123/145–149 | `DocPane.tsx:49–66` and native `useDocument.ts:18–32` key on session/path without cwd. A failed B refresh can retain A bytes. | Owning-session cwd publication invalidates old content/read generation, even without another click. Separate relative-follow-current from absolute-unavailable behavior. Delay A, fail B, recover B with one viewer mounted. |
| High: attachment recovery can happen without controller reconnect, original lines 147–149 | Remote document 503 comes from `doc_proxy.go:22–31`; controller readiness cannot announce SSH-only recovery. Host-list refresh is not a persistent subscription. | Visible/foreground transient-read demand uses single-flight capped-backoff retries, scoped to its captured source and target. Stop on success, typed terminal error, hide/background, close or replacement. Prove SSH-only restoration while controller stays ready, unrelated-host isolation and timer cleanup. |
| High: exact source pane and secondary placement are unspecified, original lines 12/24/121–127/168 | `shell/workspace.ts:322–354` puts new panes in secondary. A child conversation there is replaced by its document; current opening helpers pass no exact source-pane identity. Same-session contexts collide with Back ownership. | Jesse chose “Keep the clicked delegate visible.” Promote that exact retained pane to main, retain the prior parent and show the file in secondary. Store return ownership separately from dedup params and update it on every explicit open/reuse. Prove draft/scroll preservation, focus changes, breakpoint crossing, bound-session fallback after source close and Back with real workspace hosts. |
| Medium: file/entity overlap has no precedence, original lines 117/125 | Actual entity recognition matches the delegate ID substring inside `reports/dlg_02wMz5TxvEMoJEDTDGOTil.md`; an entity mounted before cwd hydration can prevent whole-file recognition. | Whole-file spans take precedence. Coordinate both enhancement passes' restore/invalidation on text, binding and cwd updates. Prove hydration without text changes, streaming, StrictMode and exact restoration using the real pipeline. |
| Medium: native 501 mapping contradicts the promised explanation, original lines 141/181 | Same transport/preflight gap as the backend finding. | Same distinct unsupported-capability result and actual transport proof. |
| Medium: native Markdown attributes preserve entity spelling, original lines 101/135 | Pinned renderer copies `docs/a&amp;b.md` into its link event unchanged. URI percent decoding alone addresses the wrong filename. | Native adapter resolves Markdown escapes/entities exactly once before the shared URI parser. Already-resolved web DOM destinations and literal paths do not undergo that step. Prove `&amp;`, `&amp;amp;`, escaped punctuation and percent encoding through the real pinned parser. |
| Medium: native user Reload does not exist, original lines 147–149 | `useDocument.ts:24–40` has a callback; `ReaderScreen.tsx:299–309` has no corresponding menu action. | Add Reload to existing Document actions, without a new toolbar. Prove a missing file becomes readable through the actual menu while Reader stays mounted. Update quiet-recovery documentation honestly. |
| Medium: image caching can show old bytes, original lines 14/105/151 | `/doc/image` allows 60 seconds of caching; URL lacks viewer generation. Web failed-image state is retained; native Image has no error callback. | Fresh viewer-owned image URL generation on explicit open/reopen, reload, cwd replacement and recovery. Reset failure/publication state, retain healthy same-identity content until replacement loads, preserve transcript-preview cache. Use actual browser caching and separate native qualification. |
| High hypothesis: iPhone link-hold arbitration is unproved, original lines 133/137/182 | Assistant `TimelineItem.tsx:374–407` combines parent hold actions with `selectable={false}`. The pinned renderer uses custom tap handling but UIKit link-hold delegation. No device test established behavior. | Require tap-to-open and preserve response/external actions. Handle emitted file-link hold callbacks safely, but guaranteed holds, selection changes and renderer patches are outside this first feature. Report unavailable touch/accessibility qualification; callback tests cannot prove gestures. |

## Check evidence and limits

The backend reviewer used copied production helpers. Inspection of its real-filesystem probe source and log confirms three standalone resolver cases: A→B retargeting/aliases, traversal/sibling/symlink containment, and the trusted cwd-alias spelling mismatch. These are resolver checks, not a full session/publication or endpoint test.

The client reviewer compiled the lockfile-matched native renderer parser and reported generated prose, inline-code link nodes, reference definitions, fenced exclusion and Markdown-entity URL representation. The parser output demonstrates feasibility and representation, not the future production transform or UIKit touch behavior. Its actual entity recognizer probe confirmed the delegate-ID overlap.

Evidence remains in reviewer scratch directories:

- Backend: `/tmp/evener-sandbox-2966472067/tmp/evener-sandbox-758890326`, including `filesystem-probes.log`, `typescript-probes.log` and `probes/`.
- Client: `/tmp/evener-sandbox-2966472067/tmp/evener-sandbox-1579405860`, including the preserved input spec, lockfile-matched renderer archive/source and `native-parser-probe` executable.

No feature tests, browser journey, simulator/device qualification or repository full suite has run. The native gesture finding remains a hypothesis. Cwd construction and cache-policy findings are source-confirmed; full environment-switch publication and document-read timing remain implementation proof obligations.
