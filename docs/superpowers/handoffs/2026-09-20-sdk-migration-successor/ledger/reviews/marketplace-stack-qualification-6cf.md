# Marketplace stack exact-head qualification

Read-only qualification snapshot against local `origin/main`
`6cf3f0263887914e87dbc70d558979ecea143abb`. REST PR metadata, exact-head
check-runs/status, local `git merge-base`, and the raw RoboRev panels were
checked. No code, ref, PR, push, or external comment mutation was performed.

## Verdict

**NOT QUALIFIED.** All four PRs have a pending `roborev` commit status. The
newest raw panel for every PR still has member 3 running with zero output, so
none has a completed all-member review. PR 1940 also has its `web` check
in progress. The other listed CI checks are successful.

| PR | exact head | GitHub base SHA | computed merge-base with local `origin/main` | raw panel | reviewer state |
|---|---|---|---|---|---|
| 1940 | `2989ba6916e04b1fa3eeebe2714b7dc4a33522c5` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | `bf29b025-9951-40e7-8a6a-e751b209049d` | members 0–2 done; member 3 running, job 20792 |
| 1954 | `64db435f21928a000006c420b1b9b103d1ba0c30` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | `aa9a8e47-9cd0-4058-ab4b-14a053e1615b` | members 0–2 done; member 3 running, job 20787 |
| 1960 | `a55194188d99f6cfc7a0522eeffc6883c5fa2e1e` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | `ca56c27f-3db8-4b21-b4c2-ba492ede40e7` | members 0–2 done; member 3 running, job 20782 |
| 1966 | `100e044b6bbefef69a1fd68ddd666ed130e7baa0` | `6cf3f0263887914e87dbc70d558979ecea143abb` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | `185a2730-940c-4056-ad46-42b5b3c0d80e` | members 0–2 done; member 3 running, job 20717 |

The GitHub base for 1966 is `6cf`; the actual local merge-base for all four
heads is the older `3ca` commit.

## Exact-head CI and status

REST metadata confirmed all four PRs are open, non-draft, mergeable, and point
to the exact heads above. Every check is completed-successful except
`web` on PR 1940, which is `in_progress`. The combined commit status is
`pending` for every PR because `roborev` reports `Review in progress`.

- PR 1940: 12 completed-successful checks; `web` in progress; `roborev` pending.
- PR 1954: 15 completed-successful checks; `roborev` pending.
- PR 1960: 15 completed-successful checks; `roborev` pending.
- PR 1966: 15 completed-successful checks; `roborev` pending.

## Completed raw-panel findings

PR 1940's completed members independently repeat the applied-payload consumer
seam, but that is downstream scope already carried by PR 1954 and the TUI PR
1966. The current server code also deliberately scrubs the clone path: the
manager logs the underlying removal error and returns the path-free typed
sentinel, while `cmd/evener/plugincmd_test.go:155-160` asserts that the CLI
error does not contain the clone path. The raw member's suggestion to expose
the local path conflicts with that existing contract and is not a new server
blocker. The earlier ambient-config test concern remains refuted by
`cmd/evener/testmain_test.go:46-83`, which isolates `HOME`, XDG directories,
and state before package tests run. The UID/clone-survival item (#1944), hub
reconcile logging (#1951), and typed-array coverage (#1953) remain separate
follow-ups.

PR 1954 has one completed TUI consumer Medium: the TUI must consume the typed
applied list. That is the declared downstream scope of PR 1966; members 1 and
2 found no issue in the SDK branch.

PR 1960 has two actual web consumer Mediums in the exact head:

1. `MarketplaceSheet.tsx:285-293` calls `onAppliedRemoval` for every recognized
   typed rejection, even when `writeRevisioned` retracts/discards that older
   failure because a newer mutation won. The UI can then disable the current
   entry and show a false leftover-clone warning. The store's revision fence
   (`appwire-client/typescript/state/extensions/listRevision.ts:167-169`)
   does not communicate acceptance to the sheet, and the current web tests do
   not cover the discarded-failure-to-host-marker race.
2. `index.tsx:35-91` keys the durable no-repeat guard only by marketplace name.
   An unavailable applied removal leaves the name guarded; if another client
   removes and re-adds that marketplace under the same name, the new entry is
   still blocked because the catalog-name effect cannot distinguish generations.

The native consumer Medium in this panel belongs to the unopened native lane;
it is not PR 1966. The raw member's test-isolation Medium is refuted by the
existing package-level `TestMain` isolation described above.

PR 1966 has the known, declared TUI-B fence seam, reported independently by
members 0, 1, and 2. It is already under review on the ordered-reconciliation
follow-up at `198f25075` / `c17d1b713`; it is not an unknown missing consumer.
On the exact 1966 head, an unavailable applied removal sets
`marketplaceReconcilePending` and issues one tagged list request at
`cmd/evener-tui/hub_update_config.go:548-554`, but
`handleMarketplaceListResult` clears the fence only when `msg.Err == nil`
(`cmd/evener-tui/hub_update_config.go:516-523`). A reconcile error therefore
leaves `marketplaceRemovePending` and `marketplaceReconcilePending` set,
drops later untagged lists, and blocks subsequent marketplace removals with no
retry path. Existing tests cover a successful tagged reconciliation and the
untagged-list fence, but no failed tagged reconciliation on the 1966 head.

## Raw mirrors

Completed and in-progress raw panel outputs were mirrored under the
coordinator lane:

- `reviews/raw/1940-2989ba691-remote.md`
- `reviews/raw/1954-64db435f2-remote.md`
- `reviews/raw/1960-a55194188-remote.md`
- `reviews/raw/1966-100e044b6-remote.md`

Each file includes the full completed member bodies and the running member's
zero-output status. Qualification must wait for those running reviewers to
settle and for PR 1940's `web` check to complete.
