# SDK landing sequence before native app work

This is a read-only decomposition of the AppWire SDK work in the authoritative
worktree at:

`/Users/jesse/.local/state/evener/projects/Users-jesse-git-prime-radiant-evener-fukrfUwjZk/worktrees/Users-jesse-git-prime-radiant-evener-fukrfUwjZk/live-concepts-plan2-integrate`

The worktree was at `a3fd7cdab538e9ba09643a68c55e03bc59fc86db` on
`live-concepts-plan2-integrate`, with the two pre-existing native generated-file
modifications left untouched. The source comparison uses common base
`48dcab480272cf4d48b5b62fd2c0da6e1f5fc720` against
`b9312580a11a454e304d0f59ac045f4ca7e438dc`, then independently checks each
candidate against pinned main `ab02d1cbbfd9f33b4e318105574caaa47c398e9a`.

## Recommended order

### 1. Runtime client boundary

Land the TypeScript runtime delta in
`cmd/evener-hub/frontend/src/protocol/client.ts` and its focused
`client.test.ts` cases first: `client.ts` is 111 added/2 removed lines and the
test file is 377 added/22 removed lines against pinned main.
This slice contains the strict initialize-result decoder, the handshake-result
observer, and the current-socket/current-generation publication guard. It is
buildable against the existing web AppwireClient and requires no native code,
package metadata, or recipes. Validate with the protocol client unit tests,
TypeScript typecheck, and the existing web Biome gate.

The decoder is a package-runtime boundary: generated TypeScript types alone do
not validate JSON. The observer is needed by later native/web state stores to
see every successful reconnect without confusing a stale socket for the current
one. Transport ownership remains with the existing client/transport pair; do
not add another socket owner in this PR.

Current main already uses `AppwireClient` throughout web stores and app tests.
There is no `appwire` Go delta in the common-base source comparison, so this
runtime slice has no Go prerequisite. Do not infer an SDK requirement from the
marketplace/edit differences visible in a direct pinned-main comparison.

### 2. Portable protocol helper extraction

Split helpers into two independently buildable PRs, each moving the web
consumer imports in the same change so there is no half-moved module.

* **Activity and job projection:** move
  `panes/session/chrome/activityData.ts` to
  `protocol/activityData.ts` (about 647 lines, 97% rename), add
  `protocol/activityList.ts` (about 167), `protocol/activityMerge.ts` (about
  187), and move `sessionErrors.ts` and the job-output parser (about 42 and
  35 lines). Update the activity panel/tree/row/format consumers and add the
  activity merge and job-output tests. This is roughly 1,100 helper/test lines
  plus the consumer import edits. It depends only on the existing generated
  types and client interfaces, and validates with the existing activity,
  transcript, and protocol unit tests.
* **Question answer formatting:** move
  `panes/session/composer/askDock/askCompose.ts` and its test to
  `protocol/askAnswers.ts` and `askAnswers.test.ts` (about 81 and 127 lines in
  the resulting files), export the formatter and answer types, and update
  `askShared.ts`, `AskDock.tsx`, `AskQuestionCard.tsx`, `askDockStore.ts`, and
  the ask-dock index. Keep this pure formatting slice separate from any RPC
  or UI redesign. Validate with its focused test and affected web tests.

These helpers are portable because they consume protocol data and pure
projection inputs. Browser presentation remains in the panes/stores; no React
components, native views, or browser connection state should move into the
package.

### 3. Initial package substrate and exports

Land the package shell only after the runtime client and helper moves are
stable:

- `protocol/index.ts` exports the client, generated types, helper APIs, and
  transport types (about 16 lines).
- `protocol/package.json`, `package-lock.json`, `tsconfig.build.json`, and the
  package `.gitignore`.
- `protocol/README.md` package usage and boundary documentation.
- `protocol/types.gen.ts` generated method/result/notification catalog, kept as
  one generated artifact and refreshed from the authoritative protocol source.

The generated types are already present at the common base: the exact
common-base comparison has an empty `types.gen.ts` numstat. The package shell
therefore consists of the export/build metadata, README, and qualification
boundary around that existing catalog; it is not a new 2,500-line generated
types change. It should contain no app-store/native code and no duplicate
transport implementation. The first package PR needs only a
read-only smoke example and export/type checks; it can qualify the packed
CommonJS package from a clean outside consumer before any mutating recipes are
added.

The package currently declares `dist`, `README.md`, and `examples` as package
files and exports only `dist/index`. That is a useful narrow first contract:
build, pack, install offline into a separate temporary consumer, compile both
ESM and CommonJS imports, run a tiny runtime import check, and inspect the tar
listing. Do not mix the full recipe catalog into this substrate PR.

### 4. Read-only example batches

Add examples in small capability batches, each with one logic module, one
contract module, and a thin executable module. Every batch stays opt-in to a
real hub endpoint and has deterministic contract tests through the installed
tarball.

1. **Discovery/read-only inspection:** `connection.mjs`, `inspect.mjs`,
   `discovery-*`, and the read-only portions of session listing/model catalog.
   This proves handshake, capabilities, and basic reads without credentials or
   writes (roughly 200–300 example lines).
2. **Portable projections:** `activity-*`, `job-output-*`, and `tasks-*` after
   the helper PR. These exercise traversal/projection and bounded task/job
   reads; no session mutation (roughly 500–700 lines including contracts).
3. **Read-only settings/catalog:** `commands-*`, `session-settings-*`,
   `preferences-*`, `organization-*`, `marketplaces-*`, and the read-only
   portions of `instances-*`. Keep provider credentials and edits behind
   explicit reviewed input files (roughly 700–1,000 lines including
   contracts).
4. **Bounded notifications:** `bounded-notifications`,
   `session-notifications`, `work-notifications`, `hub-notifications`, and
   `streaming-notifications`. Each observer has an explicit event/time bound
   and output policy; this is finite observation, not continuous coverage.
5. **Recovery and deliberate mutations:** `navigation-invalidation`,
   `streaming-rejoin`, `approvals`, `goals`, `queue`, `questions`,
   `session-management`, `saved-items`, `hub-upgrade`, `update`,
   `agents-doc`, `thread-force-stop`, and credentials/OAuth recipes. These
   must land as separate capability PRs because they require reviewed input,
   owned-hub checks, and explicit mutation flags. `thread-force-stop` must call
   the client's dedicated `forceStop(ref)` recovery connection rather than
   queueing an ordinary RPC. Outcomes that acknowledge dispatch while
   execution is unverified must remain explicitly qualified as such.

Each example PR updates the coverage mapping and the package qualification
runner in the same commit as its contract. The runner should enumerate the
installed package's contract modules or maintain an explicit list that includes
the new module; qualification must execute against the packed installed
consumer, not the source checkout. A package smoke PR before these batches
keeps the runner small and reviewable.

## Intermediate buildability and obsolete work

Every slice above has a concrete gate: protocol/web tests for runtime and
helpers, TypeScript build plus tarball ESM/CommonJS checks for the package
substrate, and installed-consumer contract execution for each example batch.
No slice requires the native app to import the package before it is packaged.
The native app can adopt the stable package only after the substrate and the
needed read-only batches qualify.

The source snapshot includes historical mobile handshake/reconnect commits
(`5f9cc2238`, `4212f1add`, `17594860d`, and `9f76b5f0b`). Current main already
has the web AppwireClient, its recovery transport behavior, and tests named
`slow recovery connection leaves the full request window for the hub response`,
`bypasses primary backlog, bounds overlap and closes after confirmed recovery`,
and `explicit Resume replaces the primary transport and never replays old
requests`. Retain those current-main behaviors and tests as the baseline; the
first SDK runtime PR adds only the decoder/observer boundary listed above. The
source comparison also shows no `types.gen.ts` delta from common base. The
apparent Go marketplace/edit deletions in a direct pinned-main comparison
reflect newer main state; they are not SDK landing work.

No shared state extraction is part of this sequence. Navigation caches,
activity/task/settings stores, and browser/native presentation remain consumers
until the client runtime and portable protocol modules have stable package
boundaries.
