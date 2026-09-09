# AppWire v5 rebase — 9 September 2026

Jesse requested that the native delivery branch be rebased onto updated main. The authoritative `live-concepts-plan2-integrate` worktree now contains main `48dcab480272cf4d48b5b62fd2c0da6e1f5fc720`. The rebase replayed 672 local commits and completed at `fc4f955908e3c75ba3f617df4c6ffaadfaadda44`; the reviewed Go integration is committed as `ccab2c4e0` and the installed-package SDK integration as `7055a66d9`. Native recovery is committed as `55bba80de`; the exact evidence-digest scan correction is `16ae60ac5`. Checkpoint selection remains separate.

## Preservation and conflict review

The pre-rebase head `30f2cb7c3b024c6666de055862d95f614ff908e3` is retained on `codex/iphone-before-main-20260909`. Both pre-existing generated Tauri Apple-project edits were restored and their file hashes match the originals. They remain unstaged. No original native profile, draft, reader position or session was changed during this rebase.

The relay conflicts retain main's stable-reference routing, daemon admission and lifecycle ownership checks alongside the delivery branch's startup notification holding. Saved-thread subscriptions retain the lifecycle lock and preserve `restartRequired` reads. Credential logout retains the write lock while accurately reporting whether a credential existed. The shared rail status helper retains the new restart status. Generated protocol documentation was regenerated from the combined source. Main's stronger transcript parity/allocation tests supersede the conflicting older timing test. Review and compilation identified an additional ownership-context argument. A real lifecycle-lock regression also caught a saved-subscription race: the first saved read must acquire the session lifecycle lock, while incompatible runtimes must retain a plain saved read. The corrected Go integration passes all five affected package suites.

## Contract changes

AppWire now requires **v5**, with **96 catalog methods (95 supported, one reserved)** and **37 notification names**. There is no v4 fallback.

- `turn/completed` identifies its turn through `turn.id`; its top-level `turnId` is removed.
- `restartRequired`, `resumeRequired` and `mutationStateAuthoritative` distinguish incompatible runtimes and saved snapshots from a live, authoritative mutation state.
- `evener/thread/forceStop` uses the SDK's separate recovery connection. Its server acknowledgment confirms verified daemon termination while retaining saved session data.
- `evener/update/check` and `evener/update/apply` add hub self-update operations.
- `evener/settings/agentsDoc/get`, `evener/settings/agentsDoc/set` and `evener/settings/agentsDoc/changed` expose the personal agent-instructions document.
- Provider instance editing adds rename and explicit protocol, surface and credential-source clearing fields.

Native uncertain drafts already stay retained until a positive acknowledgment or an explicit user restore/dismiss action. A saved snapshot does not prove that a mutation was absent; this migration must preserve that conservative behavior. Image input/output fields are unchanged, and same-hub image URL handling remains required.

## Validation and remaining work

The first post-rebase checks exposed the removed Go completion field, obsolete successful v4 handshake fixtures, three typed completion fixtures, and the missing lifecycle-context argument. These are corrected. Native recovery now uses the dedicated SDK helpers, retains saved-history flags, and disables composition until recovery. Resume must refresh the current destination after the SDK intentionally replaces its connection, even if that replacement disposed the initiating controls. A deterministic integration test exercises the real client, service and store against a scripted external socket; it fails without that refresh and passes with it, including navigation away before acknowledgment.

| Check | Result on the integrated source |
| --- | --- |
| Five affected Go package suites | Pass, fresh uncached run |
| `make build` | Pass |
| `make test-web` | Pass: behavior, TypeScript, Biome |
| `make test-api-package` | Pass: 35 installed contract modules, 332 tests, ESM/CommonJS and strict declarations outside checkout |
| `make test-native` | Pass: 718 tests in 76 files and TypeScript |
| Shared mobile check and focused conversation/activity/projection suites | Pass: TypeScript, Biome and 577 tests |
| Resume regression | Failure reproduced with the refresh removed; both cases pass with the correction |
| Secret scan | Pass after exempting three exact historical evidence digests; receipt files remain unchanged |
| Broad lint and full-module tests | Still being finalized; no new canonical-gate pass claimed |

The SDK contains 38 recipes for 95 supported methods, including the new personal AGENTS.md notification observer. Package qualification now fails if any declared contract module is absent and reports the actual installed consumer test totals. These deterministic contracts do not replace the real-hub outcome matrix.

Jesse requested a main checkpoint before shared-state extraction. The [checkpoint proposal](2026-09-09-iphone-main-checkpoint.md) records a staged landing scope. The complete development branch includes older prototypes and extensive historical evidence; it must not be mistaken for a small native-only diff. Main has advanced beyond the pinned rebase target, so the eventual landing candidate needs its own refresh and exact-head verification.

The installed simulator and TestFlight 0.1.0 (3) remain the earlier `f866b2a80` native artifact. Their successful dated checks remain valid for that artifact only. This rebase does not install or upload a replacement, qualify the new protocol on iPhone, or complete the SDK outcome matrix. The [project status](status.md), [remaining work](ios-v1-remaining.md) and [outcome index](sdk-outcome-matrix.md) retain those boundaries.
