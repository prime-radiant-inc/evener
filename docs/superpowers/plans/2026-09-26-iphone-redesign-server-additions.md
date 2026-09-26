# iPhone redesign, Phase 7: Server additions (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. PRs 1 to 3 are written out in full. PRs 4 to 6 and 12 are task-level: the files, interfaces and tests are named and the code is given where its shape is obvious. Every later item is a design-level section; turn each into a full plan (same format as PRs 1 to 3) just before it starts, against main as it is then.

**Goal:** The hub gives the phone (and the web and the TUI) the facts the redesign's Board, Session and Hub screens need, item by item in the roadmap's value order, so the phone can switch from each fallback as its addition lands.

**Architecture:** Most additions follow one path: a daemon fact rides the `thread/list {statusOnly:true}` root row (`server/appwire_runtime.go`), the hub's 5-second prober keeps it (`cmd/evener-hub/internal/hubcore/prober.go`) on `LiveEntry`, `BuildTree` puts it on `TreeNode`, and `projectShallow` (`cmd/evener-hub/navigation_projection.go`) puts it on `hubapi.NavigationSessionSummary`, which the shared TypeScript codec (`appwire-client/typescript/state/navigation/codec.ts`) decodes for the web and the phone. PR 1 makes that codec accept and drop value-record keys it does not know, so a field added later never breaks an older app. PR 2 makes a failed turn publish `systemError`, which the hub already shows as "errored". Items that are not row fields (activity, notices, search, documents, recipes, subagent stop) get their own methods.

**Tech Stack:** Go 1.27 workspace (root module and `agent/`, see `go.work`), AppWire over WebSocket (`ProtocolVersion` `"evener-appwire-v5"`), SQLite (`modernc` driver with FTS5, `cmd/evener-hub/internal/hubcore/past.go`), TypeScript 6 in `appwire-client/typescript` tested by vitest from `cmd/evener-hub/frontend`, React web frontend, Bubble Tea TUI (`cmd/evener-tui`), Expo/React Native phone (`mobile-native`).

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`, sections 7.2 (row anatomy), 11 (recipes), 12 (providers), 13.1 and 13.2 (attention states and counts), 16.4 (pulse meter), 17 (data sources) and 18 (server additions S1 to S14; S10 is out of scope). Roadmap: `docs/superpowers/plans/2026-09-25-iphone-redesign-roadmap.md`, phase 7 row and landing rules. Hub code map: the explorer's report on S1 to S5 and S13, whose citations are rechecked below against `8cc794480`.

## Global Constraints

- Additive wire changes stay on `ProtocolVersion = "evener-appwire-v5"` (`appwire/types.go:25`, TS `APPWIRE_PROTOCOL_VERSION` in `appwire-client/typescript/client.ts:34`); the navigation read stays `representationVersion: 2`. Every new field is optional and `omitempty`, so its key is absent unless it carries a fact, and it lands in the same PR as its codec entry, its hub schema bound and its `make generate` output (Jesse's ruling 3).
- JSON casing: `hubapi` navigation types are snake_case (`ask_pending`); `appwire` types are camelCase (`askPending`). The tagliatelle lint enforces both (`.golangci.yml`).
- A new navigation summary field is added in all of: the `hubapi` struct, `projectShallow`, `navigationSessionValueValid` or its sibling validator (`cmd/evener-hub/navigation_schema.go`), the codec's key list and validator, and the shared fixture `cmd/evener-hub/testdata/navigation/value-records.json` (PR 1, Task 1.2).
- A new pointer, slice or map field on `ProbeResult`, `LiveEntry`, `TreeNode` or a summary is deep-copied in `cloneLiveEntry` (`hubcore/roster.go:156`), `cloneNavigationLiveEntries` (`navigation_projection.go:280`), `cloneTreeNodesContext` (`hubcore/tree.go:114`) and `cloneNavigationSummary` (`navigation_projection.go:2059`) as it applies. A `LiveEntry` field that changes a row is hashed in `rosterFingerprint` (`hubcore/roster.go:377`), or the change never invalidates navigation. The navigation fingerprint is reflective and needs nothing.
- hubcore behavior tests are `fuzzScenario*` functions registered in `FuzzHubcoreScenarios` (`hubcore/scenarios_fuzz_test.go`). An unregistered one never runs; `golangci-lint run ./cmd/evener-hub/internal/hubcore/` reports it as unused (`docs/developing-evener/testing.md`, "A Test That Never Runs"). `make test` skips Fuzz targets (`scripts/gate/run-module-tests.sh:6-8`); run the seeds with `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1` (CI runs them through `make fuzz-seeds`).
- A change to what the AppWire projector (`internal/appprojector`) emits needs a `cmd/evener-tui` test case in the same PR. A new notification is dispatched by the TUI or listed in `notifyMethodsDeliberatelyIgnored` with a reason (`cmd/evener-tui/hub_notification_coverage_test.go`). A new method gets a catalog row (`appwire/protocol.go`) and passes `TestHubRouterMatchesCatalog` and `TestHubRPCRegistersExpectedHandlerSet`.
- After any `appwire` or `hubapi` type change: `make generate`, then `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1` and `make lint-generated` (both `appwire-client/typescript/types.gen.ts` and `docs/appwire-protocol.md` must be fresh).
- Go floors per module: `make vet`, and `make lint-evenerfuzz` (it runs `go vet -tags evenerfuzz ./...` for the host, Linux and Windows in every module). FIFO or syscall tests go in `*_unix_test.go`. Format with `$(go env GOROOT)/bin/gofmt`, never the `gofmt` on PATH.
- Web gate `make test-web`; phone gate `make test-native`; protocol package gate `make test-api-package`; `make lint` for the rest. Before the web gate, `cd cmd/evener-hub/frontend && npx biome check --write <touched paths>` on touched files under `src/` and `appwire-client/typescript`. Never run Biome in `mobile-native` or from the repo root, and never run `npm ci` through a symlinked `node_modules`.
- Tests are deterministic: a scripted provider at the LLM boundary, no sleeps (wait on a channel or event), and fakes only at external boundaries (`docs/developing-evener/testing.md`). Prove each new test can fail before trusting it.
- Wire names may use the codebase's own domain words (escalation, delegate); user-facing copy follows spec section 5 and belongs to the phone and web lanes.
- Each PR stays under about 800 changed lines of production code. Landing follows the roadmap: a regular PR, CI green at the head, the RoboRev comment read, /simplify run and its fixes pushed, then an admin squash merge with `--match-head-commit <full sha>`.
- This lane changes `mobile-native/` only through the shared package. Each item names the fallback it replaces; the phone lane switches off the fallback.

## Review Focus

1. **A new field the codec does not list is dropped without a sound.** After PR 1 a hub field missing from the codec's key list decodes cleanly and vanishes from every row. Pinned by Task 1.2's shared fixture, which the Go side forces to name every wire field and the TS side forces the codec to keep; every field PR extends it.
2. **A failed turn announced as closed.** The projector maps any session-end state it does not recognize to `closed` and emits `thread/closed` (`internal/appprojector/appwire_projection.go:1339-1351`), which would take the composer away from a live session. Pinned by Task 2.1's projector and server tests and Task 2.2's live-subscriber test.
3. **Stop reads as Failed.** An interrupt ends a turn without recording a failure and must stay idle. Pinned by `TestWireState_InterruptIsNotAFailedTurn` (Task 2.2).
4. **A restart erases Failed, or publishes idle first.** A daemon restarted after a failed turn must report Failed on its very first `thread/read` (the startup race of #251). Pinned by `TestRestore_FailedTurnResumesFailed` and `TestServe_FailedTurnReportsSystemErrorAcrossRestart` (Task 2.2).
5. **Resting-state controls vanish on a failed session.** The parked-queue drain, the TUI's quiet force-steer and the notes wake warning key on `idle`; a session resting on a failed turn must keep them. Pinned by Task 2.3's appwire-client, web and TUI tests.

---

## PR map

| # | PR | Spec item | Detail | Depends on |
|---|---|---|---|---|
| 1 | Tolerant navigation codec | ruling 1 | full | none |
| 2 | Failed turns settle to errored | ruling 2 | full | none |
| 3 | Approval flag on rows and attention (S2a) | S2 | full | PR 1, and a TestFlight build containing PR 1 |
| 4 | The web rail and notifications read the approval flag | S2 | task | PR 3 |
| 5 | Remote-host parity for questions and approvals (S2b) | S2 | task | PR 3 |
| 6 | Approval action and target on rows (S1a) | S1 | task | PR 5 |
| 7-8 | First pending question on rows (S1b: daemon, then hub) | S1 | design | PR 1 |
| 9 | Failure summary on rows (S1c) | S1 | design | PR 2 |
| 10-11 | Last agent message excerpt (S1d: daemon and meta, then hub) | S1 | design | PR 1 |
| 12 | Task progress for live local sessions (S13a) | S13 | task | PR 1 |
| 13 | Task progress for remote-host sessions (S13b) | S13 | design | PR 12, PR 5 |
| 14-15 | Activity pulse (S5a daemon counters, S5b hub read) | S5 | design | none |
| 16-17 | Seen-through marker (S4a turn-ended time, S4b store and method) | S4 | design | PR 1 |
| 18-19 | Subagent tallies (S3a daemon counts, S3b rows) | S3 | design | PR 1 |
| 20-21 | Scoped approvals (S12a daemon grants, S12b wire and hub) | S12 | design | none |
| 22-23 | Hub notices feed (S11a derived notices, S11b sign-in state) | S11 | design | PR 9 |
| 24-25 | Message-text search (S14a index, S14b results) | S14 | design | none |
| 26-27 | Remote document and image proxying (S7a host methods, S7b controller proxy) | S7 | design | none |
| 28 | Document revision identity | S9 | design | none |
| 29 | Hub-stored launch recipes | S8 | design | none |
| 30-31 | Direct subagent stop (S6a daemon method, S6b hub routing) | S6 | design | none |

Documents and artifacts named in the final message (the last part of S1) wait for the shared-artifacts work to reach main; no PR is planned for it here and the phone keeps its fallback (no chips).

**TestFlight checkpoint.** Phone builds made before PR 1 reject any page carrying a new key (Jesse accepted this: testers update). Before PR 3 merges, a TestFlight build that contains PR 1 must exist. The phone lane's next build covers it; if none is due, run `.github/workflows/ios-testflight.yml` once PR 1 is on main.

---

## PR 1: Tolerant navigation codec (Tasks 1.1-1.2)

**Branch:** `git fetch origin && git switch -c claude/navigation-codec-tolerance origin/main`

**What changes and why (Jesse's ruling 1).** The shared codec accepts a value record that carries a key it does not know, keeps validating the type and bounds of every key it does know, and drops the unknown key before anything installs the record. The hub's own self-check (`cmd/evener-hub/navigation_schema.go`) stays strict: it validates what the hub produces, so an unknown key there is a hub bug.

**Which records tolerate unknown keys.** The rule: a record that describes a thing is a value record and tolerates them; a record that says how to read the response or how the graph fits together is structure and stays exact.

| Record | Tolerant | Why |
|---|---|---|
| Session summary, and its running and completed jobs, watches and watch cadence | yes | Every row field the S items add lands here. |
| Project summary; the project resource's anchor value (`{key}`) | yes | Project rollups (subagent failures, unseen counts) would land here; the anchor is an entity value like the rest. |
| Pin-section descriptor | yes | A category could gain a count. |
| Manifest: its top level, each source, `attentionSummary`, `sections`, `catalogs` and each count descriptor | yes | The manifest's metadata is its content. A new attention count, a new section, a notices list or a source's version would land here, and `attentionSummary` in particular would otherwise break every older client's manifest. |
| Location metadata | yes | It describes where a session lives; an added hint must not break deep links. |
| Response envelopes and the delta's `base` | no | `status` and `representation` decide how everything else is read. A change there ships behind a new `representationVersion`. |
| Snapshot and delta records; entity, container and owner records | no | They build the graph. `codec.test.ts` already pins refusing the retired entity `revision`. |
| Entity kinds per resource | no | A row the client cannot place. |
| Paging metadata (section, pin section, pin catalog, catalog, project, project page) | no | It echoes the request and drives paging; a paging change goes behind `representationVersion` too. |

**Drop, then install.** An unknown key is removed at decode, before `normalizedGraphFromSnapshot`, `reconcileSnapshot` or `applyDelta` see it. That keeps unvalidated and unbounded data out of the graph, the rows and any cache, keeps a key from shadowing a field the client adds (`children`), and keeps `merge.ts`'s `equalJSON` identity check from re-rendering a row for a change an older app cannot show.

### Task 1.1: The codec validates known keys and drops unknown ones on value records

**Files:**
- Modify: `appwire-client/typescript/state/navigation/codec.ts` (key lists `:105-151`, validators `:153-276`, the anchor check `:298`, `descriptor` `:347-349`, `manifestMetadata` `:351-379`, the location branch of `validateResourceMetadata` `:457-469`, `decodeNavigationResponse` `:692-708`)
- Modify: `appwire-client/typescript/state/navigation/codec.test.ts` (new tests; delete the case at `:608-615` that expects an unknown session key to be refused)
- Modify: `appwire-client/typescript/README.md` (the `state/navigation` bullet, around `:116-137`)
- Modify: `cmd/evener-hub/navigation_schema.go` (doc comment on `strictNavigationDecode`, `:292`)

**Interfaces:**
- Consumes: nothing new.
- Produces: `decodeNavigationResponse(key, sentBase, wire)` keeps its signature. Its `snapshot` and `delta` results now carry only known keys on value records. Module-private helpers used by later PRs when they add keys: `interface ValueRecordKeys { required; optional; nested? }`, the constants `SESSION_KEYS`, `JOB_KEYS`, `WATCH_KEYS`, `WATCH_CADENCE_KEYS`, `PROJECT_KEYS`, `PROJECT_ANCHOR_KEYS`, `PIN_SECTION_KEYS`, `SOURCE_KEYS`, `COUNT_KEYS`, `ATTENTION_SUMMARY_KEYS`, `SECTIONS_KEYS`, `CATALOGS_KEYS`, `MANIFEST_KEYS`, `LOCATION_KEYS`. A later PR adds a session field by appending its key to `SESSION_KEYS.optional` and a check to `sessionValue`.

- [ ] **Step 1: Write the failing tests**

In `codec.test.ts`, add `materializeSnapshot` and `snapshotResource` to the `./codec` import, then add after the "codec accepts stateless records" test:

```ts
// --- Additive keys on value records ------------------------------------------
// A newer hub adds optional keys to value records without a protocol version
// bump (appwire/types.go ProtocolVersion; #1208, #2453). The codec accepts a
// value record carrying a key it does not know, still validates every key it
// does know, and drops the unknown key before anything installs the record:
// it never reaches the normalized graph, the rendered rows, or merge's
// identity comparison. Structure stays exact (the test.each below).
const futureValue = { nested: ["private-body-value"], count: -1 };

function decodedSnapshot(resource: ResourceKey, snapshot: NavigationSnapshot) {
  const decoded = decodeNavigationResponse(resource, undefined, snapshotResponse(resource, snapshot));
  if (decoded.status !== "snapshot") throw new Error(`expected a snapshot, got ${decoded.status}`);
  return decoded;
}

test("codec drops unknown keys on a session row and on its jobs, watches and cadence", () => {
  const snapshot = liveSnapshot();
  const first = snapshot.entities[0];
  if (!first) throw new Error("missing entity");
  const job = { job_id: "job-1", job_type: "shell", status: "running", task: "build" };
  const cadence = { kind: "every", seconds: 60 };
  const watch = { id: "watch-1", source: "self", deliveries: 0, created_at: "2026-09-26T12:00:00Z", active: true };
  first.value = {
    ...(first.value as object),
    running_jobs: [{ ...job, future_job_key: futureValue }],
    completed_jobs: [{ ...job, status: "completed", future_job_key: futureValue }],
    watches: [{ ...watch, cadence: [{ ...cadence, future_cadence_key: futureValue }], future_watch_key: futureValue }],
    future_session_key: futureValue,
  };
  const rows = materializeSnapshot(key, decodedSnapshot(key, snapshot)).sessions as Array<Record<string, unknown>>;
  expect(rows).toEqual([
    {
      ...sessionValue("local:session"),
      running_jobs: [job],
      completed_jobs: [{ ...job, status: "completed" }],
      watches: [{ ...watch, cadence: [cadence] }],
    },
  ]);
});

test("an unknown key never excuses a malformed known one", () => {
  const snapshot = liveSnapshot();
  const first = snapshot.entities[0];
  if (!first) throw new Error("missing entity");
  first.value = { ...(first.value as object), future_session_key: futureValue, ask_pending: "private-body-value" };
  expectContentFreeRejection(key, snapshot);
});

test("codec drops unknown keys on project, project anchor and pin-section values", () => {
  const fixtures = schemaFixtures();
  for (const kind of ["catalog", "project", "pin_catalog"] as const) {
    const fixture = fixtures.find((candidate) => candidate.key.kind === kind);
    if (!fixture) throw new Error(`missing ${kind} fixture`);
    const snapshot = cloneSnapshot(fixture.snapshot);
    for (const item of snapshot.entities) item.value = { ...(item.value as object), future_value_key: futureValue };
    for (const item of decodedSnapshot(fixture.key, snapshot).snapshot.entities)
      expect(item.value).not.toHaveProperty("future_value_key");
  }
});

test("codec drops unknown keys across the manifest and keeps everything it knows", () => {
  const manifest = schemaFixtures().find((fixture) => fixture.key.kind === "manifest");
  if (!manifest) throw new Error("missing manifest fixture");
  const source = { id: "local", label: "magic-kingdom", kind: "local", online: true };
  const known = {
    generation_id: "g",
    revision: 1,
    sources: [source],
    attentionSummary: { needsYou: 1, error: 0, working: 2 },
    sections: { live: { count: 3 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 2 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  };
  const snapshot = cloneSnapshot(manifest.snapshot);
  snapshot.metadata = {
    ...known,
    notices: futureValue,
    sources: [{ ...source, version: "private-body-value" }],
    attentionSummary: { ...known.attentionSummary, approval: 1 },
    sections: { ...known.sections, live: { count: 3, oldest: "private-body-value" }, finished: { count: 4 } },
    catalogs: { ...known.catalogs, hosts: { count: 2 } },
  };
  expect(materializeSnapshot(manifest.key, decodedSnapshot(manifest.key, snapshot))).toEqual(known);
});

test("codec drops unknown keys on a location's metadata", () => {
  const location = schemaFixtures().find((fixture) => fixture.key.kind === "location");
  if (!location) throw new Error("missing location fixture");
  const snapshot = cloneSnapshot(location.snapshot);
  snapshot.metadata = { ...(snapshot.metadata as object), host_label: "private-body-value" };
  expect(decodedSnapshot(location.key, snapshot).snapshot.metadata).not.toHaveProperty("host_label");
});

test.each([
  [
    "paging metadata",
    (snapshot: NavigationSnapshot) => {
      snapshot.metadata = { ...(snapshot.metadata as object), total: 9 };
    },
  ],
  [
    "an entity record",
    (snapshot: NavigationSnapshot) => {
      const first = snapshot.entities[0];
      if (!first) throw new Error("missing entity");
      snapshot.entities[0] = { ...first, hint: "private-body-value" } as typeof first;
    },
  ],
  [
    "a container owner",
    (snapshot: NavigationSnapshot) => {
      const owned = snapshot.containers.find((container) => container.owner.kind === "entity");
      if (!owned) throw new Error("missing owned container");
      owned.owner = { ...owned.owner, hint: "private-owner" } as typeof owned.owner;
    },
  ],
  [
    "the snapshot record",
    (snapshot: NavigationSnapshot) => {
      (snapshot as unknown as Record<string, unknown>).hint = "private-body-value";
    },
  ],
] as const)("codec still refuses an unknown key on %s", (_name, mutate) => {
  const snapshot = liveSnapshot();
  mutate(snapshot);
  expectContentFreeRejection(key, snapshot);
});

test("a delta that changes only an unknown key keeps the installed entity", () => {
  const snapshot = liveSnapshot();
  const installed = snapshotResource(key, decodedSnapshot(key, snapshot));
  const entity = snapshot.entities[0];
  if (!entity) throw new Error("missing entity");
  const decoded = decodeNavigationResponse(key, base, {
    status: "ok",
    representation: "delta",
    generationId: "g",
    revision: 2,
    etag: "tag-2",
    base,
    data: {
      metadata: { ...(snapshot.metadata as object), revision: 2 },
      upsertedEntities: [{ ...entity, value: { ...(entity.value as object), future_session_key: futureValue } }],
      removedEntityKeys: [],
      upsertedContainers: [],
      removedContainerKeys: [],
    },
  });
  if (decoded.status !== "delta") throw new Error(`expected a delta, got ${decoded.status}`);
  expect(decoded.delta.upsertedEntities[0]?.value).not.toHaveProperty("future_session_key");
  const applied = applyDelta(installed, decoded.delta, decoded.version);
  expect(applied.graph.entities.get(entity.key)).toBe(installed.graph.entities.get(entity.key));
});
```

Delete the case at `codec.test.ts:608-615` in "codec rejects wrong resource metadata, value schema, slots, scope, and orphan entities" (the one that adds `unknown: "private-body-value"` to the session value): the first new test replaces it with the opposite expectation.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation/codec.test.ts`
Expected: the four "drops unknown keys" tests and the delta test FAIL with `navigation protocol: invalid entity schema` or `invalid resource metadata`; the "still refuses" cases and "an unknown key never excuses" PASS already.

- [ ] **Step 3: Implement**

In `codec.ts`, keep `exactKeys` and put this comment above it:

```ts
// exactKeys is for structure: the response envelope, the snapshot and delta
// records, entity, container and owner records, and paging metadata. A key the
// codec does not know there changes how the rest of the response is read, so
// a change of that kind ships behind a new representationVersion.
```

After `optional`/`schemaError`, add:

```ts
// A value record's keys: the keys it must carry, the keys it may carry, and
// the keys whose values are themselves value records ("one" record, or "each"
// record of a list).
interface ValueRecordKeys {
  readonly required: readonly string[];
  readonly optional: readonly string[];
  readonly nested?: Readonly<Record<string, { readonly one: ValueRecordKeys } | { readonly each: ValueRecordKeys }>>;
}

// knownKeys is exactKeys for a value record: every required key is present,
// and a key the record's keys do not name is allowed. A newer hub adds
// optional keys to value records without a ProtocolVersion bump
// (appwire/types.go), and an app built before a key existed must keep reading
// every page that carries it. The validators still check every key they name;
// decodeNavigationResponse then drops the rest (dropUnknownKeys).
const knownKeys = (value: unknown, keys: ValueRecordKeys): value is Record<string, unknown> =>
  isRecord(value) && keys.required.every((key) => hasOwn(value, key));

// dropUnknownKeys copies a validated value record, keeping only the keys its
// ValueRecordKeys name, nested records included. It runs after validation, so
// every nested value it recurses into is a record or a list of records.
// Dropping keeps unvalidated data out of the graph and the rendered rows, and
// keeps merge's identity check from seeing a change an older app cannot show.
function dropUnknownKeys(value: Record<string, unknown>, keys: ValueRecordKeys): Record<string, unknown> {
  const known: Record<string, unknown> = {};
  for (const key of [...keys.required, ...keys.optional]) {
    if (!hasOwn(value, key)) continue;
    const item = value[key];
    const nested = keys.nested?.[key];
    if (nested === undefined) known[key] = item;
    else if ("one" in nested) known[key] = dropUnknownKeys(item as Record<string, unknown>, nested.one);
    else known[key] = (item as Record<string, unknown>[]).map((entry) => dropUnknownKeys(entry, nested.each));
  }
  return known;
}
```

Replace `SESSION_REQUIRED`, `SESSION_OPTIONAL`, `JOB_REQUIRED`, `JOB_OPTIONAL`, `WATCH_REQUIRED`, `WATCH_OPTIONAL`, `PROJECT_REQUIRED` and `PROJECT_OPTIONAL` (`:105-151`) with:

```ts
const JOB_KEYS: ValueRecordKeys = {
  required: ["job_id", "job_type", "status"],
  optional: ["command", "task", "reason", "intent", "full_command"],
};
const WATCH_CADENCE_KEYS: ValueRecordKeys = {
  required: ["kind"],
  optional: ["seconds", "derived_next_fire_at", "every", "filter"],
};
const WATCH_KEYS: ValueRecordKeys = {
  required: ["id", "source", "deliveries", "created_at", "active"],
  optional: [
    "target",
    "send_to",
    "note",
    "cadence",
    "output_match",
    "events",
    "wildcard_events",
    "delivery_times",
    "end_reason",
  ],
  nested: { cadence: { each: WATCH_CADENCE_KEYS } },
};
const SESSION_KEYS: ValueRecordKeys = {
  required: ["ref", "host_id", "session_id", "title", "project", "state", "kind", "live", "children"],
  optional: [
    "branch",
    "cluster_count",
    "favorite",
    "rename",
    "ask_pending",
    "dormant",
    "offline",
    "updated_at",
    "more_subagents",
    "omitted_descendants",
    "omitted_watches",
    "omitted_armed_watches",
    "running_jobs",
    "completed_jobs",
    "watches",
  ],
  nested: { running_jobs: { each: JOB_KEYS }, completed_jobs: { each: JOB_KEYS }, watches: { each: WATCH_KEYS } },
};
const PROJECT_KEYS: ValueRecordKeys = {
  required: ["key", "name", "session_count"],
  optional: [
    "working_dir",
    "rollup_state",
    "rollup_live",
    "rollup_attn",
    "default_expanded",
    "more_current",
    "more_recent",
    "more_archived",
    "worktrees",
    "is_archived",
    "favorite",
    "sources",
  ],
};
const PROJECT_ANCHOR_KEYS: ValueRecordKeys = { required: ["key"], optional: [] };
const PIN_SECTION_KEYS: ValueRecordKeys = { required: ["id", "name", "count"], optional: [] };
const SOURCE_KEYS: ValueRecordKeys = { required: ["id", "label", "kind", "online"], optional: [] };
const COUNT_KEYS: ValueRecordKeys = { required: ["count"], optional: [] };
const ATTENTION_SUMMARY_KEYS: ValueRecordKeys = { required: ["needsYou", "error", "working"], optional: [] };
const SECTIONS_KEYS: ValueRecordKeys = {
  required: ["live", "needs_you", "pin_sections"],
  optional: [],
  nested: { live: { one: COUNT_KEYS }, needs_you: { one: COUNT_KEYS }, pin_sections: { one: COUNT_KEYS } },
};
const CATALOGS_KEYS: ValueRecordKeys = {
  required: ["projects", "archived_projects", "test_runs"],
  optional: [],
  nested: { projects: { one: COUNT_KEYS }, archived_projects: { one: COUNT_KEYS }, test_runs: { one: COUNT_KEYS } },
};
const MANIFEST_KEYS: ValueRecordKeys = {
  required: ["generation_id", "revision", "sources", "attentionSummary", "sections", "catalogs"],
  optional: [],
  nested: {
    sources: { each: SOURCE_KEYS },
    attentionSummary: { one: ATTENTION_SUMMARY_KEYS },
    sections: { one: SECTIONS_KEYS },
    catalogs: { one: CATALOGS_KEYS },
  },
};
const LOCATION_KEYS: ValueRecordKeys = {
  required: ["generation_id", "revision", "ref", "top_level_ref", "top_level"],
  optional: ["project_key", "tier", "pin_section_id"],
};
```

Switch each value validator's key check, leaving every per-key check as it is:
- `jobValue`: `exactKeys(value, JOB_REQUIRED, JOB_OPTIONAL)` becomes `knownKeys(value, JOB_KEYS)`.
- `watchCadenceValue`: `exactKeys(value, ["kind"], [...])` becomes `knownKeys(value, WATCH_CADENCE_KEYS)`.
- `watchValue`: `knownKeys(value, WATCH_KEYS)`.
- `sessionValue`: `knownKeys(value, SESSION_KEYS)`.
- `projectValue`: `knownKeys(value, PROJECT_KEYS)`.
- `pinSectionValue`: `knownKeys(value, PIN_SECTION_KEYS)`.
- The anchor in `entityIdentityForResource` (`:298`): `!knownKeys(value.value, PROJECT_ANCHOR_KEYS) || value.value.key !== key.projectKey`.
- `descriptor`: `knownKeys(value, COUNT_KEYS) && count(value.count)`.
- `manifestMetadata`: `knownKeys(value, MANIFEST_KEYS)` for the top level, `knownKeys(source, SOURCE_KEYS)` for each source, `knownKeys(value.attentionSummary, ATTENTION_SUMMARY_KEYS)`, `knownKeys(value.sections, SECTIONS_KEYS)`, `knownKeys(value.catalogs, CATALOGS_KEYS)`.
- The location branch of `validateResourceMetadata`: `knownKeys(metadata, LOCATION_KEYS)` in place of the `exactKeys(metadata, [...], [...])` call.

Add, above `decodeNavigationResponse`:

```ts
// The value-record keys of an entity a resource holds; entity() already
// refused a kind the resource cannot hold.
function entityValueKeys(key: ResourceKey, kind: string): ValueRecordKeys {
  if (kind === "session") return SESSION_KEYS;
  if (kind === "pin_section") return PIN_SECTION_KEYS;
  return key.kind === "project" ? PROJECT_ANCHOR_KEYS : PROJECT_KEYS;
}

function knownEntity(key: ResourceKey, item: NavigationEntityRecord): NavigationEntityRecord {
  return { ...item, value: dropUnknownKeys(item.value as Record<string, unknown>, entityValueKeys(key, item.kind)) };
}

// Paging metadata is exact (validateResourceMetadata refused any unknown key),
// so only a manifest's or a location's metadata can carry one to drop.
function knownMetadata(key: ResourceKey, metadata: unknown): unknown {
  if (key.kind === "manifest") return dropUnknownKeys(metadata as Record<string, unknown>, MANIFEST_KEYS);
  if (key.kind === "location") return dropUnknownKeys(metadata as Record<string, unknown>, LOCATION_KEYS);
  return metadata;
}

function knownSnapshot(key: ResourceKey, snapshot: NavigationSnapshot): NavigationSnapshot {
  return {
    ...snapshot,
    metadata: knownMetadata(key, snapshot.metadata),
    entities: snapshot.entities.map((item) => knownEntity(key, item)),
  };
}

function knownDelta(key: ResourceKey, delta: NavigationDelta): NavigationDelta {
  return {
    ...delta,
    ...(delta.metadata === undefined ? {} : { metadata: knownMetadata(key, delta.metadata) }),
    upsertedEntities: delta.upsertedEntities.map((item) => knownEntity(key, item)),
  };
}
```

In `decodeNavigationResponse`, return the known-key copies:

```ts
  if (wire.representation === "snapshot") {
    if (!exactKeys(wire, RESPONSE_SNAPSHOT_KEYS)) throw new Error("navigation protocol: invalid snapshot");
    const snapshot = wire.data as NavigationSnapshot;
    validateSnapshotForResource(key, current, snapshot);
    return { status: "snapshot", version: current, snapshot: knownSnapshot(key, snapshot) };
  }
```

and in the delta branch `return { status: "delta", version: current, base: wire.base, delta: knownDelta(key, wire.data) };`.

Update the file's opening comment to say it "validates a hub navigation response against the resource key that asked for it, drops the value-record keys this client does not know, normalizes ...". The comment on `omittedArmedWithinOmitted` ("Mirrors navigationSessionValueValid exactly") stays true: the bounds still match.

In `appwire-client/typescript/README.md`, in the `state/navigation` bullet, after "the snapshot and delta codec (`codec`)" add: "which validates every key it knows and drops a value-record key it does not, so a field a newer hub adds never fails an older app's read,".

In `cmd/evener-hub/navigation_schema.go`, above `strictNavigationDecode`:

```go
// strictNavigationDecode refuses unknown fields. The hub validates what it
// produces, so an unknown field here is a hub bug: a map built by hand, or a
// field added to a navigation type without its bound below. The client codec
// deliberately accepts and drops unknown value-record fields
// (appwire-client/typescript/state/navigation/codec.ts), so an older app keeps
// reading pages that carry a field added after it was built; the two agree on
// every known field's bounds.
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation`
Expected: PASS, including `merge.test.ts`, `store.test.ts` and `revalidator.test.ts` unchanged.

Run: `go test ./cmd/evener-hub -run 'TestValidateNavigationResourceSnapshot' -count=1`
Expected: PASS; the hub's "unknown value field" case (`navigation_schema_test.go:58`) still refuses.

- [ ] **Step 5: Format and commit**

```bash
cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/state/navigation/codec.ts ../../../appwire-client/typescript/state/navigation/codec.test.ts && cd -
$(go env GOROOT)/bin/gofmt -l cmd/evener-hub/navigation_schema.go
git add appwire-client/typescript/state/navigation/codec.ts appwire-client/typescript/state/navigation/codec.test.ts appwire-client/typescript/README.md cmd/evener-hub/navigation_schema.go
git commit -m "feat(appwire-client): the navigation codec drops value-record keys it does not know"
```

### Task 1.2: A shared fixture keeps the hub's fields and the codec's key lists in step

**Files:**
- Create: `cmd/evener-hub/testdata/navigation/value-records.json`
- Create: `cmd/evener-hub/navigation_value_records_test.go`
- Modify: `appwire-client/typescript/state/navigation/codec.test.ts`

**Interfaces:**
- Consumes: Task 1.1's tolerant `decodeNavigationResponse`.
- Produces: the fixture every later field PR extends. The Go test fails when a navigation value type gains a JSON field the fixture lacks; the TS test fails when the codec drops a fixture field.

- [ ] **Step 1: Write the fixture**

`cmd/evener-hub/testdata/navigation/value-records.json`:

```json
{
  "session": {
    "ref": "local:01FIXTURE",
    "host_id": "local",
    "session_id": "01FIXTURE",
    "title": "Fixture session",
    "project": "evener-0123456789",
    "state": "active",
    "kind": "session",
    "branch": "main",
    "cluster_count": 2,
    "favorite": true,
    "rename": true,
    "live": true,
    "ask_pending": true,
    "dormant": true,
    "offline": true,
    "updated_at": "2026-09-26T12:00:00Z",
    "more_subagents": 3,
    "omitted_descendants": 4,
    "omitted_watches": 2,
    "omitted_armed_watches": 1,
    "running_jobs": [
      {
        "job_id": "job-1",
        "job_type": "shell",
        "status": "running",
        "command": "go test ./agent/...",
        "task": "Run the agent tests",
        "reason": "verify the fix",
        "intent": "check the settle race",
        "full_command": "go test ./agent/... -run Settle -count=1"
      }
    ],
    "completed_jobs": [
      {
        "job_id": "job-2",
        "job_type": "shell",
        "status": "completed",
        "command": "make lint",
        "task": "Lint",
        "reason": "gate",
        "intent": "check formatting",
        "full_command": "make lint LINT_TARGETS=lint-go"
      }
    ],
    "watches": [
      {
        "id": "watch-1",
        "source": "self",
        "target": "ci",
        "send_to": "self",
        "note": "wake me when CI finishes",
        "cadence": [
          {
            "kind": "events",
            "seconds": 60,
            "derived_next_fire_at": "2026-09-26T12:01:00Z",
            "every": 2,
            "filter": "status=error"
          }
        ],
        "output_match": "FAIL",
        "events": ["ci.finished"],
        "wildcard_events": true,
        "deliveries": 3,
        "delivery_times": ["2026-09-26T11:59:00Z"],
        "created_at": "2026-09-26T11:00:00Z",
        "active": true,
        "end_reason": "finished"
      }
    ],
    "children": []
  },
  "project": {
    "key": "evener-0123456789",
    "name": "evener",
    "working_dir": "/Users/jesse/git/evener",
    "rollup_state": "working",
    "rollup_live": 2,
    "rollup_attn": 1,
    "default_expanded": true,
    "more_current": 1,
    "more_recent": 2,
    "more_archived": 3,
    "worktrees": 2,
    "is_archived": true,
    "favorite": true,
    "sources": ["local", "paradise-park"],
    "session_count": 7
  },
  "pin_section": { "id": "release", "name": "Release", "count": 2 },
  "manifest": {
    "generation_id": "g",
    "revision": 1,
    "sources": [{ "id": "local", "label": "magic-kingdom", "kind": "local", "online": true }],
    "attentionSummary": { "needsYou": 4, "error": 1, "working": 9 },
    "sections": { "live": { "count": 20 }, "needs_you": { "count": 4 }, "pin_sections": { "count": 2 } },
    "catalogs": { "projects": { "count": 14 }, "archived_projects": { "count": 3 }, "test_runs": { "count": 1 } }
  },
  "location": {
    "generation_id": "g",
    "revision": 1,
    "ref": "local:01FIXTURE",
    "top_level_ref": "local:01FIXTURE",
    "project_key": "evener-0123456789",
    "top_level": true,
    "tier": "current",
    "pin_section_id": "release"
  }
}
```

The manifest and location use `generation_id` `"g"` and `revision` `1` because the TS test decodes them against the codec test's `base`.

- [ ] **Step 2: Write the Go guard**

`cmd/evener-hub/navigation_value_records_test.go`:

```go
package hub

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/hubapi"
)

// TestNavigationValueRecordFixtureNamesEveryWireField keeps the shared fixture
// testdata/navigation/value-records.json complete: it names every JSON field of
// every navigation value record, recursively, and nothing else. The client
// codec's test decodes the same file and fails if the codec drops any of it
// (appwire-client/typescript/state/navigation/codec.test.ts), so a field added
// to a type here without its codec entry fails there, where the tolerant codec
// would otherwise drop it without a sound.
func TestNavigationValueRecordFixtureNamesEveryWireField(t *testing.T) {
	raw, err := os.ReadFile("testdata/navigation/value-records.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	records := map[string]reflect.Type{
		"session":     reflect.TypeFor[hubapi.NavigationSessionSummary](),
		"project":     reflect.TypeFor[hubapi.NavigationProjectSummary](),
		"pin_section": reflect.TypeFor[hubapi.NavigationPinSectionDescriptor](),
		"manifest":    reflect.TypeFor[hubapi.NavigationManifest](),
		"location":    reflect.TypeFor[navigationLocationMetadata](),
	}
	if got, want := slices.Sorted(maps.Keys(fixture)), slices.Sorted(maps.Keys(records)); !slices.Equal(got, want) {
		t.Fatalf("fixture records = %v, want %v", got, want)
	}
	for name, typ := range records {
		assertFixtureNamesEveryField(t, name, typ, fixture[name])
	}
	// The hub's own validators accept the fixture, so the codec test decodes a
	// value the hub could really produce.
	var session hubapi.NavigationSessionSummary
	if err := strictNavigationDecode(fixture["session"], []string{"ref"}, &session); err != nil || !navigationSessionValueValid(session) {
		t.Fatalf("fixture session is not a valid hub summary (decode error %v)", err)
	}
	var manifest hubapi.NavigationManifest
	if err := json.Unmarshal(fixture["manifest"], &manifest); err != nil || !validateNavigationManifestRaw(fixture["manifest"]) || !navigationManifestValuesValid(manifest) {
		t.Fatalf("fixture manifest is not a valid hub manifest (decode error %v)", err)
	}
}

// assertFixtureNamesEveryField checks that object carries exactly typ's JSON
// field names, and recurses into every field whose type is itself a record: a
// struct, a pointer to one, or a list of them. A list must hold at least one
// element so its element type is checked too. A session's children stay empty
// (the summary schema requires it), so that list is not recursed into.
func assertFixtureNamesEveryField(t *testing.T, path string, typ reflect.Type, object json.RawMessage) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(object, &fields); err != nil {
		t.Fatalf("%s: not an object: %v", path, err)
	}
	var want []string
	for i := range typ.NumField() {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		want = append(want, name)
		raw, present := fields[name]
		if !present || name == "children" {
			continue
		}
		elem, list := field.Type, false
		for elem.Kind() == reflect.Pointer || elem.Kind() == reflect.Slice {
			list = list || elem.Kind() == reflect.Slice
			elem = elem.Elem()
		}
		if elem.Kind() != reflect.Struct || elem == reflect.TypeFor[time.Time]() {
			continue
		}
		if !list {
			assertFixtureNamesEveryField(t, path+"."+name, elem, raw)
			continue
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
			t.Fatalf("%s.%s: want a non-empty list so its element type is checked", path, name)
		}
		for index, item := range items {
			assertFixtureNamesEveryField(t, fmt.Sprintf("%s.%s[%d]", path, name, index), elem, item)
		}
	}
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(fields)); !slices.Equal(got, want) {
		t.Fatalf("%s fields = %v, want every wire field of %s: %v", path, got, typ, want)
	}
}
```

- [ ] **Step 3: Run it, then prove it can fail**

Run: `go test ./cmd/evener-hub -run '^TestNavigationValueRecordFixtureNamesEveryWireField$' -count=1 -v`
Expected: PASS. Then delete `"dormant": true,` from the fixture's session and run again: FAIL naming the missing `dormant`. Put it back.

- [ ] **Step 4: Write the TS guard**

In `codec.test.ts`, below the timestamp fixture import, add:

```ts
import valueRecordsFixture from "../../../../cmd/evener-hub/testdata/navigation/value-records.json?raw";
```

and after the other fixture constants:

```ts
const valueRecords = JSON.parse(valueRecordsFixture) as Record<
  "session" | "project" | "pin_section" | "manifest" | "location",
  Record<string, unknown>
>;
```

Then the test:

```ts
// cmd/evener-hub/navigation_value_records_test.go keeps this fixture naming
// every wire field of every navigation value record. Decoding it must keep all
// of them: a field the hub sends that the codec does not list would be dropped
// without a sound, and this is the test that hears it.
test("codec keeps every field the hub's value records carry", () => {
  const live: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
  const session = entityKey(live, "1");
  const liveDecoded = decodedSnapshot(live, {
    metadata: { generation_id: "g", revision: 1, offset: 0, limit: 50, remaining: 0, truncated: false },
    entities: [{ key: session, kind: "session", value: valueRecords.session }],
    containers: [
      {
        key: navigationRootContainerKey(live, "sessions"),
        owner: { kind: "resource_root", slot: "sessions" },
        children: [session],
      },
      {
        key: navigationOwnedContainerKey(session, "children"),
        owner: { kind: "entity", entityKey: session, slot: "children" },
        children: [],
      },
    ],
  });
  expect(liveDecoded.snapshot.entities[0]?.value).toEqual(valueRecords.session);

  const catalog: ResourceKey = { kind: "catalog", catalog: "projects", offset: 0, limit: 100 };
  const project = entityKey(catalog, "3");
  const catalogDecoded = decodedSnapshot(catalog, {
    metadata: { generation_id: "g", revision: 1, offset: 0, limit: 100, remaining: 0 },
    entities: [{ key: project, kind: "project", value: valueRecords.project }],
    containers: [
      {
        key: navigationRootContainerKey(catalog, "projects"),
        owner: { kind: "resource_root", slot: "projects" },
        children: [project],
      },
    ],
  });
  expect(catalogDecoded.snapshot.entities[0]?.value).toEqual(valueRecords.project);

  const pins: ResourceKey = { kind: "pin_catalog", offset: 0, limit: 100 };
  const pin = entityKey(pins, "2");
  const pinsDecoded = decodedSnapshot(pins, {
    metadata: { generation_id: "g", revision: 1, offset: 0, limit: 100, remaining: 0 },
    entities: [{ key: pin, kind: "pin_section", value: valueRecords.pin_section }],
    containers: [
      {
        key: navigationRootContainerKey(pins, "pin_sections"),
        owner: { kind: "resource_root", slot: "pin_sections" },
        children: [pin],
      },
    ],
  });
  expect(pinsDecoded.snapshot.entities[0]?.value).toEqual(valueRecords.pin_section);

  const manifest: ResourceKey = { kind: "manifest" };
  const manifestDecoded = decodedSnapshot(manifest, {
    metadata: valueRecords.manifest,
    entities: [],
    containers: [
      {
        key: navigationRootContainerKey(manifest, "manifest"),
        owner: { kind: "resource_root", slot: "manifest" },
        children: [],
      },
    ],
  });
  expect(manifestDecoded.snapshot.metadata).toEqual(valueRecords.manifest);

  const location: ResourceKey = { kind: "location", ref: String(valueRecords.location.ref) };
  const locationDecoded = decodedSnapshot(location, {
    metadata: valueRecords.location,
    entities: [],
    containers: [
      {
        key: navigationRootContainerKey(location, "session"),
        owner: { kind: "resource_root", slot: "session" },
        children: [],
      },
    ],
  });
  expect(locationDecoded.snapshot.metadata).toEqual(valueRecords.location);
});
```

- [ ] **Step 5: Run it, then prove it can fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation/codec.test.ts -t "keeps every field"`
Expected: PASS. Then remove `"dormant"` from `SESSION_KEYS.optional` and run again: FAIL with a diff showing `dormant` missing. Put it back.

- [ ] **Step 6: Gates and commit**

Run: `$(go env GOROOT)/bin/gofmt -l cmd/evener-hub/navigation_value_records_test.go`, `make vet`, `make lint-evenerfuzz`, `make test-web`, `make test-native`, `make test-api-package`.

```bash
git add cmd/evener-hub/testdata/navigation/value-records.json cmd/evener-hub/navigation_value_records_test.go appwire-client/typescript/state/navigation/codec.test.ts
git commit -m "test(hub): one fixture names every navigation value field for the hub and the codec"
```

- [ ] **Step 7: Open PR 1**

Title "feat(appwire-client): tolerant navigation codec (phase 7, PR 1)". The body carries the tolerance table above, says the hub's self-check stays strict, and asks for a TestFlight build containing it before PR 3 merges.

---

## PR 2: Failed turns settle to errored (Tasks 2.1-2.3)

**Branch:** `git fetch origin && git switch -c claude/failed-turn-errored origin/main`

### Semantics (the research this PR rests on)

**Today.** A turn that ends in failure settles the session to `idle` (`agent/session_state.go:292-302`, `finishProcessingAtFailureBoundary`), or to `awaiting` when a question is still pending. The daemon announces it with `EventSessionEnd{Reason: "turn_failed", State: WireState()}` (`agent/session_lifecycle.go:1700-1716`), so every client sees an idle session. `errored` exists only on the hub side: `NormalizeState` maps `systemError` to `"errored"` (`cmd/evener-hub/internal/hubcore/tree.go:563-588`), and the roster writes `"errored"` for a crashed daemon (`hubcore/roster.go:749-750`, `Crashed: true`).

**What counts as a failed turn.** A turn for which the session recorded a turn failure: `emitTurnFailure` or `emitSteeringCarrierTurnFailure` (`agent/session_events.go:265-287`), which emit `EventError` and append a `TurnFailure` record to the live history and the transcript through `recordTurn` (`agent/session.go:2128-2153`). Today's sources:
- a provider error the retry policy did not recover (`handleModelError`, `agent/session_model_call.go:819-871`: auth refused, rate limited past the retries, context overflow past recovery, a content filter hit twice);
- empty-response or bare-text retries used up (`agent/session_tool_round.go:38-51`);
- a failed skill route or skill activation admission (`agent/session_lifecycle.go:2151-2164`);
- a steering carrier whose steer could not be recorded (`agent/session_lifecycle.go:2995-3012`).

**What does not count.**
- An interrupt (Stop, `turn/interrupt`, a host cancellation). `handleModelError`'s cancel case returns before recording a failure (`:830-836`), and the round loop records an interrupt marker (a `TurnSteering` of kind `interrupted`) and settles idle (`agent/session_lifecycle.go:1303-1340`).
- A tool call that errors: the model sees the error result and the turn goes on.
- Hitting `MaxTurns` or `MaxToolRoundsPerInput`: `EventTurnLimit`, resumable, no failure record (`:2478-2492`, `:2610-2630`).
- A turn refused before it started (a closed session, an unhealthy transcript, an admission failure): nothing is recorded.
- A crashed daemon: it stays the hub's own `errored` with `Crashed: true`, unchanged.

**The state.** The session's internal state does not change: a failed session is `SessionIdle` inside and takes the next message like any idle session, so no `s.state == SessionIdle` check anywhere moves. The failure is a projection. `RestingWireState()` reads `systemError` when the session is idle, or awaiting with no pending question, and its history ends in a failure: walking back from the newest record, skipping bookkeeping records (system, checkpoint, summary, model switch, hook completed, environment, notes context, attention resolution), the first turn-bearing record is a `TurnFailure`. `WireState()` keeps its override on top: a resting session with autonomous work pending (job notifications, claimable queued input) reads `active`, because that work starts the next turn without the user.

**How it clears.** By the next turn starting. The user's next message (`TurnUserInput`), a steer or a note carried as steering (`TurnSteering`), and a turn that a job notification or goal kick starts (its assistant or tool-results records) all end the "history ends in a failure" condition; while that turn runs the session reads `active` anyway. A model switch, a hook line or an environment record leaves it failed. Compacting a failed session's history can fold the failure record away and clear it; compaction is a user action, so that is acceptable.

**Precedence.**
- A closed session reads `closed`; a running turn reads `active`.
- A pending question reads `awaiting` whatever else holds. Answering it is what moves the session, and the failure stays readable in the transcript; this is also today's behavior.
- Otherwise a history that ends in a failure reads `systemError`, raised to `active` while autonomous work is pending, since that work starts the next turn.
- Otherwise today's rules hold: idle is raised to `active` by pending autonomy, and awaiting is never raised.

**Restart.** The rule is a function of state, pending asks and history under one lock, and restore rebuilds all three from the transcript, so a restarted daemon derives the same answer the live one published. That follows the repo's standing rule that live settle and restore derivation agree (`deriveRestoredState`, `agent/session_tools_ask.go:454-538`). The restored daemon stamps it on its `SessionStart` event and publishes it before its first read.

**Downstream, already in place.** `appStatus` passes `systemError` through (`server/appwire_runtime.go:3020-3048`), and its capability set keeps `Send` (`appCapabilitiesLocked`, `:2843`); the hub's local source passes it through (`internal/appsource/local_daemon.go:1293-1313`); `NormalizeState` makes it `"errored"`; `attentionLevel("errored")` is `"error"` (`hubcore/attention.go:9-20`), counted in `AttentionSummary.Error`, which the phone's Needs you count adds (spec 13.2); the NeedsYou tier puts errored first (`hubapi.NeedsYouBand`, `hubapi/attention.go:91-99`) and `promotedAttentionLevel` never downgrades it; the web rail says "failed" (`appwire-client/typescript/railSessionState.ts:36`) and its desktop notifications treat error rows as loud (`frontend/src/notifications/attention.ts:49-60`); the TUI's `stateLabel` maps it to "errored" (`cmd/evener-tui/hub_dashboard_view.go:569`); `deriveSendQueueAvailability` gives plain Send (`appwire-client/typescript/sendQueueAvailability.ts:90`). A live failed subagent shows "errored" under its coordinator (`runningSubagentState`, `tree.go:978`) and never counts toward Needs you (`tierEligible`).

**What must change.** The projector (Task 2.1), the agent and the daemon's startup write (Task 2.2), and the resting-state gates that compare to `idle` (Task 2.3). No wire type changes: `systemError` is an existing `ThreadStatus` value, so `ProtocolVersion` stays.

**Deliberately unchanged.** The hub relay's synthesized frames for a lost daemon connection keep `idle` (`cmd/evener-hub/app_relay.go:1790-1840`): the relay cannot tell a dead daemon from a network drop, and the roster marks a dead one errored on its next refresh. Only its comment, which describes the daemon's own failure frame, changes.

**Expect after deploy.** Once running daemons restart onto this build, sessions whose last turn failed and was never retried appear in Needs you as Failed. That is the intended honesty; archiving clears them.

### Task 2.1: The projector carries a failed session's state through

**Files:**
- Modify: `internal/appprojector/appwire_projection.go` (the `EventSessionStart` state switch `:303-309`, the `EventSessionEnd` state switch `:1342-1351`)
- Test: `internal/appprojector/appwire_projection_test.go`
- Test: `server/appwire_capabilities_push_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `EventSessionStart{State: "systemError"}` projects `thread/started` and `thread/status/changed` with `systemError`; `EventSessionEnd{State: "systemError"}` projects `thread/status/changed(systemError)` and no `thread/closed`.

- [ ] **Step 1: Write the failing tests**

In `appwire_projection_test.go`, after `TestAppEventProjectorMapsAwaitingSessionEnd`:

```go
// TestAppEventProjectorMapsFailedSessionEnd: a failed turn ends its input with
// EventSessionEnd{Reason: "turn_failed", State: systemError} (the agent's
// endInputAtTurnFailure). The session is open and takes the next message, so
// the projector announces systemError and never thread/closed.
func TestAppEventProjectorMapsFailedSessionEnd(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{
		Reason: "turn_failed",
		State:  appwire.ThreadStatusSystemError,
	}})

	if hasAppNotification(sessionEnd, appwire.NotifyThreadClosed) {
		t.Fatalf("a failed turn's SessionEnd emitted thread/closed: %+v", sessionEnd)
	}
	if status := notificationThreadStatus(t, sessionEnd, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusSystemError {
		t.Fatalf("failed SessionEnd status = %+v, want systemError", status)
	}
}

// TestAppEventProjectorRestoredSessionStartCarriesFailedState: a daemon
// restored onto a transcript that ends in a failed turn stamps systemError on
// its SessionStart (agent RestingWireState), and the thread starts Failed.
func TestAppEventProjectorRestoredSessionStartCarriesFailedState(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "th_1",
		Data:      events.SessionStartData{Profile: "openai", Model: "gpt-5", Restored: true, State: appwire.ThreadStatusSystemError},
	})

	if thread := notificationThread(t, started, appwire.NotifyThreadStarted); thread.Status.Type != appwire.ThreadStatusSystemError {
		t.Fatalf("restored SessionStart thread status = %+v, want systemError", thread.Status)
	}
	if status := notificationThreadStatus(t, started, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusSystemError {
		t.Fatalf("restored SessionStart status notification = %+v, want systemError", status)
	}
}
```

In `server/appwire_capabilities_push_test.go`, after `TestStatusChangeCarriesTheCapabilitiesForThatStatus` (it also needs `"primeradiant.com/evener/appwire"` in its imports):

```go
// A session resting on a failed turn is open: its status frame says
// systemError, carries Send so the composer offers the next message, and the
// daemon never announces it closed.
func TestFailedTurnStatusFrameKeepsTheSessionOpen(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	wireRetrySafeCapabilities(srv)

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "go"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "turn_failed", State: appwire.ThreadStatusSystemError}})

	statuses := statusNotifications(t, srv, "th_1")
	failed := statuses[len(statuses)-1]
	if failed.Status.Type != appwire.ThreadStatusSystemError {
		t.Fatalf("last status = %q, want systemError", failed.Status.Type)
	}
	if failed.Capabilities == nil || !failed.Capabilities.Send {
		t.Fatalf("failed status capabilities = %+v, want Send true: the session takes the next message", failed.Capabilities)
	}
	for _, n := range srv.AppNotificationsAfter(0, "th_1") {
		if n.Notification.Method == appwire.NotifyThreadClosed {
			t.Fatalf("a failed turn announced thread/closed: %+v", n.Notification)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/appprojector ./server -run 'TestAppEventProjectorMapsFailedSessionEnd|TestAppEventProjectorRestoredSessionStartCarriesFailedState|TestFailedTurnStatusFrameKeepsTheSessionOpen' -count=1`
Expected: FAIL: the end projects `closed` plus `thread/closed`, and the start projects `idle`.

- [ ] **Step 3: Implement**

In the `EventSessionStart` switch:

```go
		case appwire.ThreadStatusSystemError:
			// A restored session whose transcript ends in a failed turn
			// (agent RestingWireState).
			status = appwire.ThreadStatusSystemError
```

In the `EventSessionEnd` switch:

```go
		case appwire.ThreadStatusSystemError:
			// Open and resting on a failed turn (agent RestingWireState): the
			// session takes the next message, so this is no close.
			state = appwire.ThreadStatusSystemError
```

The switch must name every value `WireState` can publish, not just `systemError`. A turn that fails while a message waits in the queue ends with `State: "active"`: the failure path returns before the drain ladder (`agent/session_lifecycle.go` about `:1440-1443`), and `endInputAtTurnFailure` (about `:1700-1716`) publishes the effective state, which is `active` while that queued turn is about to run. Mapping it to `closed` would announce a closed session. Only a real close (`""` or `closed`) projects `thread/closed`. Add a projector test for a failed-turn end with pending work (`State: "active"`) that asserts no `thread/closed`, and the matching `cmd/evener-tui` case.

`server/bridge.go` needs nothing: `sessionEventClosesSession` treats only `""` and `closed` as closing (`:177-183`), and `sessionEventStatusEffect` stores the state it is given (`:199-231`).

- [ ] **Step 4: Run them to verify they pass, then the packages**

Run: `go test ./internal/appprojector ./server -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
$(go env GOROOT)/bin/gofmt -l internal/appprojector server
git add internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go server/appwire_capabilities_push_test.go
git commit -m "feat(appprojector): a session resting on a failed turn stays open as systemError"
```

### Task 2.2: A failed turn publishes systemError until the next turn, across a restart

**Files:**
- Modify: `agent/session_state.go` (`WireState` `:54-74`; add `RestingWireState` and `historyEndsInTurnFailure`; imports `slices` and `primeradiant.com/evener/appwire`)
- Modify: `agent/session_init.go` (the restored `SessionStart` state, `:1566-1580`)
- Modify: `cmd/evener/serve.go` (the startup write, `:1672`)
- Create: `agent/session_turn_failure_state_test.go`
- Modify: `agent/session_lifecycle_test.go` (`TestSession_GenuineTurnFailureEmitsSessionEndRestoringIdleStatus` `:272` and `TestSession_GenuineTurnFailureNotifiesLiveSubscriberOfIdleStatus` `:332`)
- Modify: `cmd/evener/serve_model_switch_test.go` (`TestServeModelSwitch_ProviderFailureRestoresCapability` `:156`)
- Create: `cmd/evener/serve_failed_turn_test.go`

**Interfaces:**
- Consumes: Task 2.1's projector mapping.
- Produces: `func (s *Session) RestingWireState() string` (exported for `cmd/evener`), `func historyEndsInTurnFailure(history []schema.Turn) bool`, and `WireState()` returning `appwire.ThreadStatusSystemError` for a session resting on a failed turn.

- [ ] **Step 1: Write the failing tests**

`agent/session_turn_failure_state_test.go`:

```go
package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestHistoryEndsInTurnFailure(t *testing.T) {
	t.Parallel()
	turns := func(kinds ...schema.TurnKind) []schema.Turn {
		out := make([]schema.Turn, len(kinds))
		for i, kind := range kinds {
			out[i] = schema.Turn{Kind: kind}
		}
		return out
	}
	cases := []struct {
		name    string
		history []schema.Turn
		want    bool
	}{
		{"empty history", nil, false},
		{"a clean turn", turns(schema.TurnUserInput, schema.TurnAssistant), false},
		{"a failed turn", turns(schema.TurnUserInput, schema.TurnFailure), true},
		{"a failed turn with a salvaged draft and its explanation", turns(schema.TurnUserInput, schema.TurnAssistant, schema.TurnSteering, schema.TurnFailure), true},
		{"bookkeeping after the failure", turns(schema.TurnUserInput, schema.TurnFailure, schema.TurnModelSwitch, schema.TurnHookCompleted, schema.TurnEnvironment, schema.TurnNotesContext, schema.TurnSystem, schema.TurnCheckpoint, schema.TurnSummary, schema.TurnAttentionResolution), true},
		{"the next message after a failure", turns(schema.TurnFailure, schema.TurnUserInput), false},
		{"a steer or interrupt marker after a failure", turns(schema.TurnFailure, schema.TurnSteering), false},
		{"a turn a notification started after a failure", turns(schema.TurnFailure, schema.TurnSystem, schema.TurnAssistant), false},
		{"tool results after a failure", turns(schema.TurnFailure, schema.TurnToolResults), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := historyEndsInTurnFailure(c.history); got != c.want {
				t.Fatalf("historyEndsInTurnFailure = %v, want %v", got, c.want)
			}
		})
	}
}

// failingThenRecoveringSession's first model call fails with a provider error
// that is not retried; every later call answers "recovered".
func failingThenRecoveringSession(t *testing.T, dir string) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeErrAdapter{name: "openai", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, "sign-in rejected", nil, nil)
		},
		func(llm.Request) (llm.Response, error) { return finalResponse("recovered"), nil },
	}})
	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, LLMRetryPolicy: &policy})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// A turn that records a failure leaves the session idle inside (it takes the
// next message) and systemError on the wire, until the next turn starts.
func TestWireState_FailedTurnReadsFailedUntilTheNextTurn(t *testing.T) {
	t.Parallel()
	sess := failingThenRecoveringSession(t, t.TempDir())
	defer sess.Close()
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err == nil {
		t.Fatal("first turn succeeded, want the scripted provider failure")
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("State after a failed turn = %q, want idle: the session still takes input", got)
	}
	if got := sess.WireState(); got != appwire.ThreadStatusSystemError {
		t.Fatalf("WireState after a failed turn = %q, want %q", got, appwire.ThreadStatusSystemError)
	}
	if _, err := sess.ProcessInput(ctx, "second", nil); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := sess.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("WireState after the next clean turn = %q, want awaiting", got)
	}
}

// An interrupt ends a turn without recording a failure: Stop is not Failed.
func TestWireState_InterruptIsNotAFailedTurn(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	blocked := make(chan struct{})
	c.Register(&blockingAdapter{name: "openai", blocked: blocked})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	// TRIPWIRE: blockingAdapter is an in-process fake; only fires on a genuine hang.
	outer, outerCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer outerCancel()
	turnCtx, stop := context.WithCancel(outer)
	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(turnCtx, "hello", nil)
		done <- err
	}()
	<-blocked
	stop()
	<-done
	if got := sess.WireState(); got != string(SessionIdle) {
		t.Fatalf("WireState after Stop = %q, want idle: an interrupt is not a failed turn", got)
	}
}

// A turn that fails while a question is still pending (a human-note carrier's
// turn, which does not answer ask1) stays awaiting on the wire: answering the
// question is what moves the session.
func TestWireState_PendingQuestionOutranksAFailedTurn(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeErrAdapter{name: "openai", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) { return toolCallResponse(ask), nil },
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, "carrier provider failure", nil, nil)
		},
	}})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if _, err := sess.SetHumanNote("note-question-outranks-failure", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err == nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v, want a provider failure after the carrier ran", ran, err)
	}
	if got := sess.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("WireState with a pending question after a failed turn = %q, want awaiting", got)
	}
}

// A daemon restarted after a failed turn derives the same Failed state from
// its transcript that the live session published, and stamps it on its
// SessionStart event, so a restart never turns a failure into idle.
func TestRestore_FailedTurnResumesFailed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := failingThenRecoveringSession(t, dir)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err == nil {
		t.Fatal("first turn succeeded, want the scripted provider failure")
	}
	id := sess.ID()
	sess.Close()

	meta, err := schema.LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	c2 := llm.NewClient()
	c2.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c2, withTestSessionNamer(c2, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	eventsPtr, mu, doneCh := collectEvents(restored)
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored State = %q, want idle", got)
	}
	if got := restored.WireState(); got != appwire.ThreadStatusSystemError {
		t.Fatalf("restored WireState = %q, want %q", got, appwire.ThreadStatusSystemError)
	}
	restored.Close()
	<-doneCh
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *eventsPtr {
		if d, ok := ev.Data.(events.SessionStartData); ok && ev.Kind == events.EventSessionStart {
			if d.State != appwire.ThreadStatusSystemError {
				t.Fatalf("restored SessionStart State = %q, want %q", d.State, appwire.ThreadStatusSystemError)
			}
			return
		}
	}
	t.Fatal("restored session emitted no SessionStart")
}
```

In `agent/session_lifecycle_test.go`:
- Rename `TestSession_GenuineTurnFailureEmitsSessionEndRestoringIdleStatus` to `TestSession_GenuineTurnFailureEmitsSessionEndWithFailedStatus` and want `found.State == appwire.ThreadStatusSystemError` (`:316-318`).
- Rename `TestSession_GenuineTurnFailureNotifiesLiveSubscriberOfIdleStatus` to `TestSession_GenuineTurnFailureNotifiesLiveSubscriberOfFailedStatus`. Count `systemError` frames in place of `idle` ones (`:402-410`), want `systemError` for the frame right after `turn/completed(Failed)` (`:420-423`), and add: the stream holds exactly one `thread/closed`, the one `Close()` produces (the projector before Task 2.1 made a second).
- Update both doc comments: the failure exit now announces `systemError`.

In `cmd/evener/serve_model_switch_test.go`, `TestServeModelSwitch_ProviderFailureRestoresCapability` waits for an `idle` frame carrying `ChangeModel` after the failed turn (`:271-285`) and asserts `thread/read` reports `idle` (`:312-314`). After this task the failed session rests `systemError`, so the milestone matches `params.Status.Type == appwire.ThreadStatusSystemError` (renamed "resting with model capability") and the read wants `systemError`. What the test pins, `ChangeModel` restored after a provider failure, is unchanged.

`cmd/evener/serve_failed_turn_test.go`, modeled on `TestServeAsk_RestoreReportsAwaitingImmediately` (`cmd/evener/serve_ask_test.go:394`):

```go
package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestServe_FailedTurnReportsSystemErrorAcrossRestart drives the real daemon:
// a turn the provider fails leaves thread/read reporting systemError, and a
// daemon resumed on that session reports systemError on its very FIRST read
// (serve.go publishes RestingWireState synchronously before the bridge drains
// SessionStart, the #251 race), never idle first.
func TestServe_FailedTurnReportsSystemErrorAcrossRestart(t *testing.T) {
	workDir := t.TempDir()
	stateDir := t.TempDir()
	runDir := t.TempDir()
	installServeScriptedProvider(t, &scriptedProvider{
		name: "openai",
		errorSteps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, "sign-in rejected", nil, nil)
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	firstArgs := []string{"--model", "openai/gpt-test", "--addr", "127.0.0.1:0", "--dir", workDir, "--state-dir", stateDir, "--run-dir", runDir}
	done1 := make(chan error, 1)
	go func() { done1 <- runServe(firstArgs) }()
	entry1 := waitForServeTestRendezvous(t, runDir)

	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry1.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "serve-failed-turn-test", Version: "test"}}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: entry1.SessionID}.String()
	if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
		ClientMutationID:   "failed-turn",
		ExpectedInstanceID: entry1.SessionID,
		Ref:                ref,
		Input:              []appwire.InputItem{{Type: "text", Text: "do the thing"}},
	}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	pollServeAskStatusUntil(t, entry1.Address, appwire.ThreadStatusSystemError, 10*time.Second, 100*time.Millisecond)
	client.Close()

	if err := shutdownServeTestDaemon(context.Background(), entry1.Address, entry1.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}
	select {
	case err := <-done1:
		if err != nil {
			t.Fatalf("first runServe returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first runServe did not exit after thread/shutdown")
	}

	secondArgs := []string{"--model", "openai/gpt-test", "--addr", "127.0.0.1:0", "--resume", entry1.SessionID, "--dir", workDir, "--state-dir", stateDir, "--run-dir", runDir}
	done2 := make(chan error, 1)
	go func() { done2 <- runServe(secondArgs) }()
	entry2 := waitForServeTestRendezvous(t, runDir)
	if first := waitForServeAskStatusUp(t, entry2.Address, 10*time.Second); first.State != appwire.ThreadStatusSystemError {
		t.Fatalf("first thread/read after restore = %q, want %q", first.State, appwire.ThreadStatusSystemError)
	}
	if err := shutdownServeTestDaemon(context.Background(), entry2.Address, entry2.SessionID); err != nil {
		t.Fatalf("thread/shutdown (second daemon): %v", err)
	}
	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("second runServe returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second runServe did not exit after thread/shutdown")
	}
}
```

The `time.After` selects are tripwire bounds on an awaited exit, the same shape the template uses.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./agent -run 'TestHistoryEndsInTurnFailure|TestWireState_|TestRestore_FailedTurnResumesFailed|TestSession_GenuineTurnFailure' -count=1`
Expected: FAIL to compile (`historyEndsInTurnFailure` undefined); after a stub returning false, the failed-state assertions fail with `idle`.

Run: `go test ./cmd/evener -run '^TestServe_FailedTurnReportsSystemErrorAcrossRestart$' -count=1`
Expected: FAIL, the poll times out still reading `idle`.

- [ ] **Step 3: Implement**

In `agent/session_state.go`, replace `WireState` and its doc comment. The precedence paragraph keeps its rule and its `TestWireState_AwaitingOutranksAutonomy` pin; only its subject widens from idle to a resting state.

```go
// WireState is the externally-reported session state: RestingWireState, with
// one override. A resting session (idle, or resting on a failed turn) with
// undelivered job notifications or claimable queued input reads as "active",
// because work the session owns can start its next turn without user input. A
// queue parked by a Stop is not claimable and reads as resting -- nothing will
// move it until the user acts (kata wms7). Live child activity belongs to the
// child's wire state, not the settled parent's.
//
// Precedence: the override raises a resting state ONLY. A session awaiting
// its user projects as awaiting even with autonomy in flight: a session that
// asked its user (ask-user-question design) cannot proceed without them, and
// masking the question as "working" would deadlock, since the wakes that
// could move the session are gated behind the very answer the user was never
// told to give. TestWireState_AwaitingOutranksAutonomy pins this.
//
// The two reads take their own locks in turn, never nested, as WireState
// always has: sessionWorkPending's signals each take their own lock (the
// settle lock discipline, autonomyInFlight), and a change between the reads
// publishes again through the session's state events.
func (s *Session) WireState() string {
	state := s.RestingWireState()
	if (state == string(SessionIdle) || state == appwire.ThreadStatusSystemError) && s.sessionWorkPending() {
		return string(SessionProcessing)
	}
	return state
}

// RestingWireState is State() with one substitution: a session resting on a
// failed turn publishes appwire.ThreadStatusSystemError, which every client
// shows as Failed. It rests on a failed turn when it is idle, or awaiting with
// no pending question, and its history ends in a recorded turn failure
// (historyEndsInTurnFailure). A pending question keeps awaiting: answering it
// is what moves the session, and the failure stays readable in the
// transcript. The next turn to start ends the failure, since it records a
// turn-bearing entry; an interrupt never records a failure at all.
//
// It reads state, the pending asks and the history under one lock, and
// restore rebuilds all three from the transcript, so a restored session
// derives the answer the live session published. RestoreSession stamps it on
// its SessionStart event and serve publishes it before the first turn, both
// before the restored work-pending signals exist; WireState adds that
// override on top.
func (s *Session) RestingWireState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	resting := s.state == SessionIdle || s.state == SessionAwaiting
	if resting && len(s.askPending) == 0 && historyEndsInTurnFailure(s.history) {
		return appwire.ThreadStatusSystemError
	}
	return string(s.state)
}

// historyEndsInTurnFailure reports whether the last turn-bearing record in
// history is a recorded turn failure (a TurnFailure, written by
// emitTurnFailure and emitSteeringCarrierTurnFailure): the session's last turn
// failed and no turn has started since. A turn-bearing record is one a turn
// writes as it runs: user input, steering (the interrupt marker included), an
// assistant response, tool results. Bookkeeping records (TurnSystem,
// TurnCheckpoint, TurnSummary, TurnModelSwitch, TurnHookCompleted,
// TurnEnvironment, TurnNotesContext, TurnAttentionResolution) are skipped, so
// a model switch or a hook line after the failure leaves the session failed.
func historyEndsInTurnFailure(history []schema.Turn) bool {
	for i := range slices.Backward(history) {
		switch history[i].Kind {
		case schema.TurnFailure:
			return true
		case schema.TurnUserInput, schema.TurnSteering, schema.TurnAssistant, schema.TurnTool, schema.TurnToolResults:
			return false
		}
	}
	return false
}
```

In `agent/session_init.go`, the restored `SessionStart` carries the effective wire state: `State: s.WireState(),` in place of `State: string(restoredState),` (`:1580`). The restore path takes and releases `s.mu` (about `:1568-1571`) before `emitSessionStartEnvelope` runs (about `:1573-1580`), so the locking `WireState` is the right read there. A lock-free read would race, and holding the lock across the emit won't work because the emit takes session locks itself. The test must observe the `SessionStart` emitted during restore: subscribe before restoring if the API allows it, otherwise verify it through the daemon-level startup test. `recomputeRestoredState` may later upgrade idle to awaiting; `RestingWireState` gives `systemError` for both when the history ends in a failure, so the published value cannot go stale.

In `cmd/evener/serve.go:1672`: `srv.SetState(sess.WireState())` in place of `srv.SetState(string(sess.State()))`. The restored `SessionStart` carries the same `WireState`, so the synchronous startup write and the bridge's event write agree whichever lands last (#251). A restored failed session whose restored queue already holds claimable work publishes `active` in both. Find where the restored work queues become known relative to these writes. If they arrive later, publish the state again when they do, so a restored failed session with claimable work reads `active` once its queue is known, never `systemError` for good. Add a restart test for a restored failed session with pending work that asserts the final published state is `active`.

- [ ] **Step 4: Run them to verify they pass, then the packages**

Run: `go test ./agent -count=1 -run 'WireState|Restore|TurnFailure|Awaiting|AskUser_LiveState'` then `go test ./agent ./cmd/evener ./server ./internal/appprojector -count=1`
Expected: PASS. Tests that compare a failed input's session-end state to `WireState()` (`agent/session_environment_rollback_regression_test.go:742-748`, `agent/session_drain_as_steer_turn_boundary_test.go:383`) pass unchanged; the ask tests that want `turn_failed/Awaiting` pass because a pending question keeps awaiting; tests that check `State()` after a failure (`agent/session_model_test.go:2309`, `:2752`) pass because the internal state is unchanged. Any other test that asserts `idle` on the wire after a recorded failure now sees `systemError`: update its expectation and name it in the commit body.

- [ ] **Step 5: Commit**

```bash
$(go env GOROOT)/bin/gofmt -l agent/session_state.go agent/session_init.go agent/session_turn_failure_state_test.go agent/session_lifecycle_test.go cmd/evener/serve.go cmd/evener/serve_failed_turn_test.go cmd/evener/serve_model_switch_test.go
git add agent/session_state.go agent/session_init.go agent/session_turn_failure_state_test.go agent/session_lifecycle_test.go cmd/evener/serve.go cmd/evener/serve_failed_turn_test.go cmd/evener/serve_model_switch_test.go
git commit -m "feat(agent): a session resting on a failed turn reports systemError until its next turn"
```

The commit body says the two `TestSession_GenuineTurnFailure*` tests and `TestServeModelSwitch_ProviderFailureRestoresCapability` changed their expected state from idle to systemError, and why.

### Task 2.3: Resting-state controls hold for a failed session

**Files:**
- Modify: `appwire/status.go` (add `IsRestingThreadStatus`); Test: `appwire/status_test.go`
- Modify: `cmd/evener-tui/composer_panel.go:86`, `cmd/evener-tui/hub_session_keys.go:539`, `cmd/evener-tui/hub_commands.go:1238-1240`, `cmd/evener-tui/hub_notifications.go` (comment `:224-234`)
- Test: `cmd/evener-tui/hub_model_test.go` (`TestHubModelFailedTurnSettlesOnItsStatusFrame` `:2510`, and a new test), `cmd/evener-tui/hub_appwire_test.go` (`TestHubModelTurnCompletedReconcilesProcessingForFailedTurn` `:414`)
- Modify: `appwire-client/typescript/submitRouting.ts` (`:126-174`), `appwire-client/typescript/index.ts` (the `./submitRouting` export block, `:395-412`); Test: `appwire-client/typescript/submitRouting.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/NotesPanel.tsx:160`; Test: `NotesPanel.test.tsx`
- Modify: `cmd/evener-hub/app_relay.go` (comment `:1804-1806` only)

**Interfaces:**
- Consumes: Task 2.2's `systemError` on a resting session.
- Produces: `appwire.IsRestingThreadStatus(status string) bool` (Go) and `isSessionResting(statusType: string): boolean` (TS root export): `idle` or `systemError`.

- [ ] **Step 1: Write the failing tests**

`appwire/status_test.go`:

```go
func TestIsRestingThreadStatus(t *testing.T) {
	for _, status := range []string{ThreadStatusIdle, ThreadStatusSystemError} {
		if !IsRestingThreadStatus(status) {
			t.Errorf("%q should be resting", status)
		}
	}
	for _, status := range []string{ThreadStatusActive, ThreadStatusAwaiting, ThreadStatusWarning, ThreadStatusClosed, ThreadStatusNotLoaded, ThreadStatusRestartRequired, ""} {
		if IsRestingThreadStatus(status) {
			t.Errorf("%q should not be resting", status)
		}
	}
}
```

`cmd/evener-tui/hub_model_test.go`, retarget `TestHubModelFailedTurnSettlesOnItsStatusFrame` to the frame the daemon now sends: the status frame's `Type` becomes `appwire.ThreadStatusSystemError`, the state assertion wants `systemError`, and its doc comment says the failure exit announces `thread/status/changed(systemError)`. Then add:

```go
// A failed turn leaves the session resting like an idle one: the daemon
// reports systemError until the next turn starts. The resting affordances
// keyed on idle hold for it too (appwire.IsRestingThreadStatus).
func TestHubModelSessionRestingOnAFailedTurnKeepsRestedControls(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{
		Ref:          "local:th_1",
		SessionID:    "sess_1",
		Live:         true,
		State:        appwire.ThreadStatusSystemError,
		Capabilities: hubSessionCapabilities{Send: true, Steer: true, Queue: true},
	}
	if c := m.sessionControls(); !c.send || c.stop || c.drain {
		t.Fatalf("controls on a failed session with nothing queued = %+v, want send only", c)
	}
	if !hubNotesIdleWake(m.detail) {
		t.Fatal("a live session resting on a failed turn must warn that saving a note wakes the agent")
	}
	before := len(m.session.messages)
	updated, _ := m.handleSessionForceSteer()
	if got := updated.(hubModel); len(got.session.messages) != before {
		t.Fatalf("force-steer on a resting failed session with nothing queued added %+v, want the quiet no-op an idle session gets", got.session.messages[before:])
	}
	m.detail.Queue.Depth = 1
	if c := m.sessionControls(); !c.drain {
		t.Fatalf("controls with a parked queue on a failed session = %+v, want drain offered", c)
	}
}
```

In `cmd/evener-tui/hub_appwire_test.go`, `TestHubModelTurnCompletedReconcilesProcessingForFailedTurn` sends the `systemError` frame in place of `idle` and its comment says so; its assertions (processing false, composer out of queue mode) stand.

`submitRouting.test.ts`, after "a queue parked by Stop (idle, depth > 0) ...":

```ts
// A failed turn leaves the session resting like an idle one: the daemon
// reports systemError until the next turn starts (agent RestingWireState).
test("a session resting on a failed turn offers send, and drain for a parked queue", () => {
  expect(sessionControls("systemError", ALL, 0)).toMatchObject({
    stop: false,
    steer: false,
    drain: false,
    queue: false,
    send: true,
  });
  const parked = sessionControls("systemError", ALL, 2);
  expect(parked).toMatchObject({ drain: true, drainQueue: true, send: true });
  expect(parked.reason.drain).toBeUndefined();
});
```

`NotesPanel.test.tsx`, after "idle live session shows the wake warning under the editor":

```tsx
test("a live session resting on a failed turn shows the wake warning too", () => {
  openPanel(testModel({ status: { type: "systemError" }, humanNote: "old note" }));
  expect(screen.getByTestId("shared-notes-idle-wake").textContent).toMatch(/Saving will wake the agent/);
});
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./appwire ./cmd/evener-tui -run 'TestIsRestingThreadStatus|TestHubModelSessionRestingOnAFailedTurnKeepsRestedControls|TestHubModelFailedTurnSettlesOnItsStatusFrame|TestHubModelTurnCompletedReconcilesProcessingForFailedTurn' -count=1`
Expected: FAIL for `TestIsRestingThreadStatus` (undefined) and the new resting-controls test (the TUI withholds the drain and the wake warning, and force-steer reports "no active turn"). The two retargeted frame tests already pass, since the TUI applies whatever status a frame carries; they are the TUI case this PR's projector change owes.

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/submitRouting.test.ts src/panes/session/chrome/NotesPanel.test.tsx`
Expected: the two new cases FAIL.

- [ ] **Step 3: Implement**

`appwire/status.go`:

```go
// IsRestingThreadStatus reports a session resting between turns: idle, or
// resting on a failed turn (systemError, which a daemon reports from a failed
// turn until the next turn starts). Controls that belong to a rested session
// -- releasing a queue a Stop parked, the note-save wake warning -- apply to
// both.
func IsRestingThreadStatus(status string) bool {
	return status == ThreadStatusIdle || status == ThreadStatusSystemError
}
```

TUI: `composer_panel.go:86` becomes `parked := appwire.IsRestingThreadStatus(m.detail.State) && m.detail.Queue.Depth > 0`; `hub_session_keys.go:539` becomes `if appwire.IsRestingThreadStatus(m.detail.State) && m.detail.Queue.Depth == 0 && !m.queueRevisionStale {`; `hubNotesIdleWake` returns `detail.Live && appwire.IsRestingThreadStatus(detail.State)` and its comment says "resting". In `hub_notifications.go:224-234`, the comment says the failure exit is announced as `thread/status/changed(systemError)`.

`submitRouting.ts`, above `sessionControls`:

```ts
// A session resting between turns: idle, or resting on a failed turn (the
// daemon reports systemError from a failed turn until the next turn starts;
// agent/session_state.go RestingWireState). A queue a Stop parked is the
// user's to release from either.
export function isSessionResting(statusType: string): boolean {
  return statusType === "idle" || statusType === "systemError";
}
```

`sessionControls` uses `const parked = isSessionResting(statusType) && queueDepth > 0;` and `canDrainQueue` returns `isTurnActive(statusType) || (isSessionResting(statusType) && queueDepth > 0)`. Export `isSessionResting` from `index.ts` beside `isTurnActive`.

`NotesPanel.tsx`: import `isSessionResting` from `@evener/appwire-client` and use `const idleWake = live && isSessionResting(model.status.type);`.

`app_relay.go:1804-1806`: the comment says the daemon's own failure exit announces `thread/status/changed(systemError)`, and that the relay's synthesized frame keeps `idle` because a lost connection is not a recorded failure and the roster marks a dead daemon errored.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run the two commands from Step 2: PASS. Then `go test ./appwire ./cmd/evener-tui -count=1`, `cd cmd/evener-hub/frontend && npx biome check --write src/panes/session/chrome/NotesPanel.tsx src/panes/session/chrome/NotesPanel.test.tsx ../../../appwire-client/typescript/submitRouting.ts ../../../appwire-client/typescript/submitRouting.test.ts ../../../appwire-client/typescript/index.ts`, `make test-web`, `make test-native`, `make test-api-package`, `make vet`, `make lint-evenerfuzz`, `make lint`.

- [ ] **Step 5: Commit and open PR 2**

```bash
$(go env GOROOT)/bin/gofmt -l appwire cmd/evener-tui cmd/evener-hub/app_relay.go
git add appwire/status.go appwire/status_test.go cmd/evener-tui/composer_panel.go cmd/evener-tui/hub_session_keys.go cmd/evener-tui/hub_commands.go cmd/evener-tui/hub_notifications.go cmd/evener-tui/hub_model_test.go cmd/evener-tui/hub_appwire_test.go appwire-client/typescript/submitRouting.ts appwire-client/typescript/submitRouting.test.ts appwire-client/typescript/index.ts cmd/evener-hub/frontend/src/panes/session/chrome/NotesPanel.tsx cmd/evener-hub/frontend/src/panes/session/chrome/NotesPanel.test.tsx cmd/evener-hub/app_relay.go
git commit -m "feat: resting-state controls hold for a session resting on a failed turn"
```

Open PR 2, "feat(agent): failed turns settle to errored (phase 7, PR 2)", with the Semantics section above as its body, including "Expect after deploy".

---

## PR 3: The approval flag on rows and in attention, S2a (Tasks 3.1-3.2)

**Branch:** `git fetch origin && git switch -c claude/s2a-approval-flag origin/main`. PR 1 is on main, and a TestFlight build containing it exists.

**What it adds.** A session blocked on a sandbox approval already counts in Needs you: `promotedAttentionLevel` promotes it (`hubcore/attention.go:31-37`) and the NeedsYou tier includes it (`tree.go:1517`), while its row keeps its real state `"active"` (pinned by `tree_test.go:2192-2193`). Nothing on the row or the attention entry says why. This PR adds `approval_pending` to the navigation summary and `approvalPending` to the attention entry. `AttentionSummary` is unchanged: approvals already count in `needsYou`. The phone's fallback (subscribe to Needs you sessions to find escalations) can then go.

### Task 3.1: Tree rows and attention entries carry the approval

**Files:**
- Modify: `cmd/evener-hub/internal/hubcore/tree.go` (`TreeNode` `:424-463`; an `approvalPendingFor` closure beside `askPendingFor` `:1011-1013`; `buildNode` `:1176-1203`; the live-only leaf `:1430-1444`; the NeedsYou node `:1533-1542`)
- Modify: `cmd/evener-hub/internal/hubcore/attention.go` (`DeriveAttention` `:92`; `Tick` `:138`, `:150-151`)
- Modify: `appwire/attention.go` (`AttentionEntry`)
- Test: `cmd/evener-hub/internal/hubcore/attention_test.go`, `cmd/evener-hub/internal/hubcore/tree_live_agreement_test.go`, `cmd/evener-hub/internal/hubcore/tree_test.go`, `cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: `LiveEntry.PendingEscalation` (existing).
- Produces: `TreeNode.ApprovalPending bool`; `appwire.AttentionEntry.ApprovalPending bool` with JSON `approvalPending,omitempty` (TS `AttentionChanged.approvalPending?: boolean`).

- [ ] **Step 1: Write the failing scenarios**

`attention_test.go`:

```go
// fuzzScenarioDeriveAttention_CarriesApprovalPending: the attention entry says
// why an escalation-promoted session needs you, beside the promotion, so a
// client can show an approval.
func fuzzScenarioDeriveAttention_CarriesApprovalPending(t *testing.T) {
	metas := []schema.SessionMeta{{ID: "01A", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	live := []LiveEntry{{SessionID: "01A", Status: appwire.ThreadStatusActive, PendingEscalation: true}}
	entries, _ := DeriveAttention(metas, live, nil)
	if got := entries["01A"]; !got.ApprovalPending || got.Level != "needs_you" {
		t.Fatalf("entry = %+v, want ApprovalPending at level needs_you", got)
	}
	live = []LiveEntry{{SessionID: "01A", Status: appwire.ThreadStatusActive}}
	entries, _ = DeriveAttention(metas, live, nil)
	if entries["01A"].ApprovalPending {
		t.Fatalf("entry without an escalation carries ApprovalPending: %+v", entries["01A"])
	}
}

// fuzzScenarioAttentionWatcher_TicksOnApprovalOnlyFlip: level and ask can hold
// still while the approval moves; a client keyed on the approval must hear it,
// and a session that goes away clears it.
func fuzzScenarioAttentionWatcher_TicksOnApprovalOnlyFlip(t *testing.T) {
	var got []appwire.AttentionChangedPayload
	w := NewAttentionWatcher(func(p appwire.AttentionChangedPayload) { got = append(got, p) })
	w.Tick(map[string]appwire.AttentionEntry{"01A": {ID: "01A", Level: "needs_you", AskPending: true}}, appwire.AttentionSummary{})
	w.Tick(map[string]appwire.AttentionEntry{"01A": {ID: "01A", Level: "needs_you", AskPending: true, ApprovalPending: true}}, appwire.AttentionSummary{})
	if len(got) != 1 || !got[0].Changed[0].ApprovalPending {
		t.Fatalf("payloads = %+v, want one change carrying ApprovalPending", got)
	}
	w.Tick(map[string]appwire.AttentionEntry{}, appwire.AttentionSummary{})
	if len(got) != 2 || got[1].Changed[0].ApprovalPending {
		t.Fatalf("payloads = %+v, want the gone entry with ApprovalPending cleared", got)
	}
}
```

`tree_live_agreement_test.go`:

```go
// fuzzScenarioBuildTree_EveryRowCarriesApprovalPending: an escalation-promoted
// session reports the approval on its NeedsYou, Live and project rows alike,
// from the one approvalPendingFor closure, and keeps its real state on all of
// them: promotion changes membership, not state.
func fuzzScenarioBuildTree_EveryRowCarriesApprovalPending(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{ID: "01APPROVAL", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}
	live := []LiveEntry{{PID: 1, SessionID: "01APPROVAL", Status: appwire.ThreadStatusActive, PendingEscalation: true}}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	if len(tree.NeedsYou) != 1 || !tree.NeedsYou[0].ApprovalPending || tree.NeedsYou[0].State != "active" {
		t.Fatalf("NeedsYou = %+v, want one active row carrying ApprovalPending", tree.NeedsYou)
	}
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01APPROVAL")
	if !inLive || !inProject {
		t.Fatalf("session missing: live=%v project=%v", inLive, inProject)
	}
	if !liveRow.ApprovalPending || !projectRow.ApprovalPending {
		t.Fatalf("Live row %v, project row %v: both must carry the approval", liveRow.ApprovalPending, projectRow.ApprovalPending)
	}
}
```

`tree_test.go`, beside `fuzzScenarioLiveTier_CarriesAskPendingFromLiveEntry`:

```go
// fuzzScenarioLiveTier_LiveOnlyLeafCarriesApprovalPending: a live session the
// past index has not caught up with is built as a meta-less leaf, and that
// builder carries the approval too.
func fuzzScenarioLiveTier_LiveOnlyLeafCarriesApprovalPending(t *testing.T) {
	live := []LiveEntry{{PID: 1, SessionID: "01NOMETA", Status: appwire.ThreadStatusActive, PendingEscalation: true}}
	tree := buildTree(nil, live)
	if len(tree.Live) != 1 || !tree.Live[0].ApprovalPending {
		t.Fatalf("Live = %+v, want the meta-less leaf carrying ApprovalPending", tree.Live)
	}
}
```

Register all four in `FuzzHubcoreScenarios`, in alphabetical order.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`
Expected: FAIL to compile (`ApprovalPending` undefined).

- [ ] **Step 3: Implement**

`appwire/attention.go`, in `AttentionEntry` after `AskPending`:

```go
	// ApprovalPending is true while the session is blocked on a sandbox
	// escalation a human must allow or deny (M7). It is why an
	// escalation-promoted session's Level is needs_you; AskPending is the
	// question's equivalent.
	ApprovalPending bool `json:"approvalPending,omitempty"`
```

`tree.go`, in `TreeNode` after `AskPending`:

```go
	// ApprovalPending is true while the daemon reports a blocked
	// sandbox-exemption escalation (LiveEntry.PendingEscalation). Like
	// AskPending, every builder sets it from one closure so a session's rows
	// agree, and it never changes State: promotedAttentionLevel changes
	// membership only.
	ApprovalPending bool
```

Beside `askPendingFor`:

```go
	// approvalPendingFor resolves the pending-approval marker for a session ID
	// from the same live map, for the same reason askPendingFor does.
	approvalPendingFor := func(id string) bool {
		return liveMap[id].PendingEscalation
	}
```

In `buildNode`: `approvalPending := approvalPendingFor(m.ID)` after `askPending`; `approvalPending = false` inside `if parentDead`; `ApprovalPending: approvalPending,` in the literal. In the live-only leaf: `ApprovalPending: approvalPendingFor(le.SessionID),`. In the NeedsYou node: `ApprovalPending: le.PendingEscalation,` beside `AskPending: le.PendingAsk,`.

`attention.go`: `e := appwire.AttentionEntry{ID: le.SessionID, Level: level, AskPending: le.PendingAsk, ApprovalPending: le.PendingEscalation}`; the `Tick` condition adds `|| prev.ApprovalPending != e.ApprovalPending`; the gone branch adds `gone.ApprovalPending = false`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files, then `make generate`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub/internal/hubcore -count=1`, `golangci-lint run ./cmd/evener-hub/internal/hubcore/` (no unused scenario), `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`
Expected: PASS; `types.gen.ts` gains `approvalPending?: boolean;` on `AttentionChanged`.

- [ ] **Step 5: Commit**

```bash
git add appwire/attention.go cmd/evener-hub/internal/hubcore/tree.go cmd/evener-hub/internal/hubcore/attention.go cmd/evener-hub/internal/hubcore/attention_test.go cmd/evener-hub/internal/hubcore/tree_live_agreement_test.go cmd/evener-hub/internal/hubcore/tree_test.go cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): tree rows and attention entries say a session waits on an approval"
```

### Task 3.2: Navigation rows carry approval_pending

**Files:**
- Modify: `hubapi/navigation.go` (`NavigationSessionSummary`, after `AskPending` `:238`)
- Modify: `cmd/evener-hub/navigation_projection.go` (`projectShallow` `:1742-1782`)
- Modify: `appwire-client/typescript/state/navigation/codec.ts` (`SESSION_KEYS.optional`, `sessionValue`)
- Modify: `cmd/evener-hub/testdata/navigation/value-records.json` (session record)
- Create: `cmd/evener-hub/navigation_approval_test.go`
- Test: `appwire-client/typescript/state/navigation/codec.test.ts`
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: `TreeNode.ApprovalPending` (Task 3.1).
- Produces: `hubapi.NavigationSessionSummary.ApprovalPending bool` with JSON `approval_pending,omitempty`; TS `NavigationSessionSummary.approval_pending?: boolean`. PR 4 and the phone lane read it.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/navigation_approval_test.go`:

```go
package hub

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
)

// TestNavigationRowsCarryApprovalPending pins S2's wire contract: a live
// session blocked on a sandbox approval carries approval_pending on its Live
// and NeedsYou rows and keeps reporting its real state ("active"). Every other
// row omits the key, so its shaping is byte-for-byte unchanged.
func TestNavigationRowsCarryApprovalPending(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "evener")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 1, SessionID: "01APPROVAL", WorkingDir: project.CanonicalPath}, SessionID: "01APPROVAL", Status: appwire.ThreadStatusActive, PendingEscalation: true},
		hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 2, SessionID: "01WORKING", WorkingDir: project.CanonicalPath}, SessionID: "01WORKING", Status: appwire.ThreadStatusActive},
	)
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Roster: roster})
	_, live, projects := web.navigationTreeInputs(t.Context())
	tree := hubBuildNavigationTree(nil, live, map[hubcore.ArchiveKey]bool{}, projects)
	inputs := navigationBuildInputsFromTreeSnapshot("generation", 1, tree, web.apiTreeSources(), hubapi.AttentionSummary{}, live, nil, nil, nil, nil)
	projection, err := buildNavigationProjection(inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}

	needsYou := projection.NeedsYouPage(0, 50).Sessions
	if len(needsYou) != 1 || needsYou[0].SessionID != "01APPROVAL" {
		t.Fatalf("needs-you rows = %#v, want only the session blocked on an approval", needsYou)
	}
	if !needsYou[0].ApprovalPending || needsYou[0].State != "active" {
		t.Fatalf("needs-you row = %#v, want approval_pending with its real state active", needsYou[0])
	}
	liveRows := projection.LivePage(0, 50).Sessions
	if len(liveRows) != 2 {
		t.Fatalf("live rows = %#v, want both sessions", liveRows)
	}
	for _, row := range liveRows {
		raw, carried := navigationSummaryJSONFields(t, row)["approval_pending"]
		if row.SessionID == "01APPROVAL" {
			if string(raw) != "true" {
				t.Fatalf("approval row JSON approval_pending = %q, want true (row = %#v)", raw, row)
			}
		} else if carried {
			t.Fatalf("row %s carries approval_pending; want the key absent: %#v", row.SessionID, row)
		}
	}
}
```

`codec.test.ts`:

```ts
test("codec keeps the approval flag on a session row and refuses a non-boolean one", () => {
  const withApproval = (approval: unknown) => {
    const snapshot = liveSnapshot();
    const first = snapshot.entities[0];
    if (!first) throw new Error("missing entity");
    first.value = { ...(first.value as object), state: "active", approval_pending: approval };
    return snapshot;
  };
  const rows = materializeSnapshot(key, decodedSnapshot(key, withApproval(true))).sessions as Array<
    Record<string, unknown>
  >;
  expect(rows[0]?.approval_pending).toBe(true);
  expectContentFreeRejection(key, withApproval("private-body-value"));
});
```

Add `"approval_pending": true,` to the fixture's session record after `"ask_pending": true,`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestNavigationRowsCarryApprovalPending|TestNavigationValueRecordFixtureNamesEveryWireField' -count=1`
Expected: FAIL to compile (`ApprovalPending` undefined on the summary), then the fixture guard names `approval_pending` as extra until the struct has it.

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation/codec.test.ts`
Expected: "keeps the approval flag" and "keeps every field" FAIL: the codec drops the key.

- [ ] **Step 3: Implement**

`hubapi/navigation.go`, after `AskPending`:

```go
	// ApprovalPending is true while the session is blocked on a sandbox
	// escalation a human must allow or deny (M7). The row keeps its real State
	// ("active": the escalation blocks mid-turn); the flag says why the session
	// is in NeedsYou, beside AskPending for a question.
	ApprovalPending bool `json:"approval_pending,omitempty"`
```

`projectShallow`: `ApprovalPending: node.ApprovalPending,` after `AskPending`. No schema bound is needed for a bool; `strictNavigationDecode` accepts the new struct field.

`codec.ts`: add `"approval_pending"` to `SESSION_KEYS.optional` after `"ask_pending"`, and `optional(value.approval_pending, bool) &&` to `sessionValue` after the `ask_pending` check.

Run `$(go env GOROOT)/bin/gofmt -w hubapi/navigation.go cmd/evener-hub/navigation_projection.go`, then `make generate`.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run the two commands from Step 2: PASS. Then `go test ./cmd/evener-hub/... -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`, `make lint-generated`, `make vet`, `make lint-evenerfuzz`, the Biome write on the two TS files, `make test-web`, `make test-native`, `make test-api-package`, `make lint`. No TUI case is needed: the projector is untouched, the TUI reads no navigation rows, and `evener/attention/changed` is on its deliberately-ignored list.

- [ ] **Step 5: Commit and open PR 3**

```bash
git add hubapi/navigation.go cmd/evener-hub/navigation_projection.go cmd/evener-hub/navigation_approval_test.go cmd/evener-hub/testdata/navigation/value-records.json appwire-client/typescript/state/navigation/codec.ts appwire-client/typescript/state/navigation/codec.test.ts appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): navigation rows carry approval_pending"
```

PR 3, "feat(hub): the approval flag on rows and attention (S2a, phase 7 PR 3)". The body names the fallback it retires and links the TestFlight build that contains PR 1.

---

## PR 4: The web rail and notifications read the approval flag (S2's web half, task level)

**Adds.** Spec 18's S2 row: approvals stop reading as "working" on the web rail. An active row with `approval_pending` presents as needing you everywhere the rail judges attention, glossed "approval waiting", and the web's attention notifications treat an approval like a question (loud).

**Files and changes:**
- `appwire-client/typescript/railSessionState.ts`: `humanizeState(wireState, askPending, approvalPending = false)`; when `approvalPending` and the state is not `"errored"`, it returns `"approval waiting"` before the switch. The phone's current `screens.tsx` calls keep two arguments. Test: `railSessionState.test.ts` (`humanizeState("active", false, true)` is `"approval waiting"`; `humanizeState("errored", false, true)` is `"failed"`).
- `cmd/evener-hub/frontend/src/shell/rail/railNodes.ts`: `displayState(node)` (`:607-610`) returns `"awaiting"` for a row with `approval_pending === true` whose state is not `"errored"`, so the dot, the needs-you badge count and the sort all see it. Test: `railNodes.test.ts`.
- `cmd/evener-hub/frontend/src/shell/rail/RailRow.tsx`: pass `session.approval_pending === true` into every `humanizeState` call (`:237`, `:241`, `:464`), and keep an approval row out of the active-work override (`:657`), which today would turn its dot green. Test: `RailRow.test.tsx`, an active row with `approval_pending` renders the needs-you signal and the "approval waiting" gloss.
- `cmd/evener-hub/frontend/src/notifications/attention.ts`: `AttentionEntry` gains `approvalPending: boolean`; `snapshotFromNavigation` counts an `approval_pending` row as `needs_you`; `detectFires` is loud for `approvalPending`. `notifications/index.ts:85` maps `approvalPending: changed.approvalPending === true` beside `askPending`. Test: `attention.test.ts`.

**Gates.** Biome on the touched files, `make test-web`, `make test-native` (the shared `railSessionState.ts`), `make test-api-package`.

---

## PR 5: Remote-host parity for questions and approvals (S2b, task level)

**Adds.** A remote host's sessions carry `ask_pending` and `approval_pending` like local ones. Today `appThreadTreeEntries` (`cmd/evener-hub/web_api_tree.go:811-864`) builds the remote `LiveEntry` without `AskPending` or the escalation, so remote rows never show a question or an approval and are never promoted, and the remote hub's own list rows carry `AskPending` but no escalation cards (`internal/appsource/local_daemon.go:1118-1144`).

**Files and changes:**
- `cmd/evener-hub/internal/hubcore/roster.go` and `prober.go`: `ProbeResult` and `LiveEntry` gain `PendingEscalations []appwire.SandboxEscalationRequested`, the blocked cards in raise order. Set it wherever `PendingEscalation` is set, from the same source (`prober.go:152-153`, `liveEntryFromProbe` `roster.go:1271-1290`, the spawned-daemon read `roster.go:1350`), so the bool stays `len(PendingEscalations) > 0`. Copy the slice in `cloneLiveEntry` and `cloneNavigationLiveEntries` (the card holds only strings and a bool, so an element copy is a deep copy). Hash each card's `EscalationID` in `rosterFingerprint` after the `PendingEscalation` byte (`roster.go:408-414`), so a replaced card invalidates navigation for PR 6.
- `cmd/evener-hub/internal/appsource/local_daemon.go`: `LocalDaemonEntry` gains `PendingEscalations`; `threadFromEntry` sets `Evener.PendingEscalations` on root rows.
- `cmd/evener-hub/app_rpc.go` `localDaemonEntriesFromRoster` (`:116-160`): copy the cards for root entries. Clear `PendingAsk`, `PendingEscalation` and `PendingEscalations` on descendant aliases: they are cloned from the root entry (`child := entry`, `:138`) and today inherit the root's ask, which remote parity would turn into a visible bug. Subagents never ask the user or escalate (`agent/session_escalation.go:134`).
- `cmd/evener-hub/web_api_tree.go` `appThreadTreeEntries`: `entry.PendingAsk = thread.Evener.AskPending`, and the cards and the bool from `thread.Evener.PendingEscalations`.
- Wire: none new; the remote hub's `thread/list` rows now carry the existing `pendingEscalations` field.

**Tests:** `TestAppThreadTreeEntriesCarryRemoteAskAndApproval` (`web_api_tree_test.go`); a navigation test in the style of `navigation_offline_test.go` where a `RemoteThreadCache` row with `AskPending` and one card projects `ask_pending` and `approval_pending` and joins NeedsYou; `TestLocalDaemonEntriesFromRosterAliasesInheritNoAskOrApproval`; a `threadFromEntry` test (cards on a root row, none on an alias); `fuzzScenarioStatusProber_DecodesPendingEscalation` (`prober_test.go:188`) extended to check the probe keeps the card; roster tests that the fingerprint moves when the first card's ID changes and that `cloneLiveEntry` does not alias the cards.

**Gates.** `make vet`, `make lint-evenerfuzz`, `go test ./cmd/evener-hub/... -count=1`, `golangci-lint run ./cmd/evener-hub/internal/hubcore/`, `make lint`.

---

## PR 6: The approval's action and target on rows (S1a, task level)

**Adds.** The row's why line for an approval: "Approval · wants to write outside the workspace: ~/sites/docs" (spec 7.1, 13.1). The hub carries the oldest pending card's tool and denied path; the phone maps the tool to a verb.

**Files and changes:**
- `hubcore/tree.go`: `TreeNode` gains `ApprovalTool` and `ApprovalTarget string`, set in the three builders from a `firstApprovalFor(id)` closure over `liveMap[id].PendingEscalations[0]` (raise order, so the oldest), cleared for a dead parent.
- `hubapi/navigation.go`: `ApprovalTool string` with JSON `approval_tool,omitempty` and `ApprovalTarget string` with JSON `approval_target,omitempty`. The doc comment says the target is the escalation's full literal path, shown for informed consent (`appwire/types.go:969-978`), and reaches human clients only, as `thread/read`'s cards already do.
- `projectShallow`: `truncateNavigationBytes(node.ApprovalTool, maxNavigationIdentityBytes)` and `truncateNavigationRunes(node.ApprovalTarget, maxNavigationLabelRunes)`.
- `navigation_schema.go` `navigationSessionValueValid`: `navigationSchemaIdentity(value.ApprovalTool, true)` and `utf8.RuneCountInString(value.ApprovalTarget) <= maxNavigationLabelRunes`.
- `codec.ts`: both keys in `SESSION_KEYS.optional`; `optional(value.approval_tool, (item) => identity(item))` and `optional(value.approval_target, (item) => boundedString(item, 512))`.
- Fixture: both keys on the session record. `make generate`.

**Tests:** a hubcore scenario (two cards; every row carries the first card's tool and target), the navigation test from PR 3 extended (both keys present on the approval row, absent elsewhere), a schema test beside `TestNavigationSessionValueValidatesOmittedWatches` (`navigation_schema_test.go:387`) refusing a 513-rune target, a projection test that a 600-rune target arrives truncated to 512, and codec keep and refuse cases.

---

## S1b-S1d: The rest of the row "why" payload (design level)

S1 is split three more ways because each part needs agent and daemon state the hub does not have (`askPending` carries no text, no error text reaches the hub, no excerpt is tracked; explorer report section 3). The documents-and-artifacts part waits for the shared-artifacts work.

### S1b: The first pending question (PRs 7-8)

- **Adds.** "Question · keep or drop the implied options?" on the row, and the option labels for the long-press preview.
- **Daemon (PR 7).** `askQuestion` keeps `Header` and `Question` only (`agent/session_tools_ask.go:33-36`); keep the option labels too, from the parse that already sees them (`:278-289`), in `s.askPending` and in `deriveRestoredAskPending`'s restore path. A new envelope field on `EvenerThread`: `PendingQuestion *PendingQuestion` with `{header, question, options []string, count int}`, where `count` is how many questions the ask holds ("Question 1 of 2"). It needs a new `ThreadEnvelopeSource` method, which `server/thread_envelope.go:112` calls a concurrency decision: sample it on the carriers that already move `AskPending` (the ask posted, answered or cleared), never on deltas. Deep-copy in `appwire/clone.go` (`cloneEvenerThread` `:104-117`).
- **Hub (PR 8).** The StatusOnly root row carries it; the probe keeps it on `LiveEntry` (clone, fingerprint); a `TreeNode` field; the summary gains `question *NavigationQuestion{text, options, count}` with bounds text at most 512 runes, at most 5 options of at most 200 runes each, count at most 4. Codec nested record, fixture, `make generate`.
- **Fallback.** "Has a question"; the phone subscribes to the few Needs you sessions.
- **Tests.** Agent: labels kept live and after restore. Server: the StatusOnly row carries the question and it clears on answer. Hub: prober wire test, fingerprint, navigation test, schema bounds, codec.
- **Open questions.** None that change behavior; the text a row shows is the first question, per spec 7.2.

### S1c: The failure summary (PR 9)

- **Adds.** "Failed · codex-jesse-fsck.com sign-in expired (401)" on a Failed row.
- **Daemon.** After PR 2 the failure is the newest `TurnFailure` in the history, which carries `TurnFailureInfo{Message, Title, Hint, Cause{Kind, Provider, Model, Status}}` (`agent/schema/turn.go:135-180`). A new envelope field, `EvenerThread.Failure *ThreadFailure{title, message, causeKind, provider}`, present exactly when the resting wire state is `systemError` and derived from the history the same way (one function beside `historyEndsInTurnFailure`), so it agrees across a restart.
- **Hub.** The probe keeps it; the summary gains `failure *NavigationFailure{title, message, cause_kind, provider}` with title and message at most 200 runes. A crashed daemon (`Crashed: true`) carries `cause_kind: "crashed"` from the hub so the phone can say so.
- **Fallback.** A bare "Failed".
- **Tests.** Agent: the envelope carries the failure after a failed turn, clears on the next turn, and survives a restore. Hub: probe, fingerprint, navigation, schema, codec.
- **Open questions.** None; it feeds S11's sign-in notices.

### S1d: The last agent message excerpt (PRs 10-11)

- **Adds.** The Finished row's why line: the opening of the last agent message, about 200 characters, in the reading serif (spec 7.2).
- **Daemon and meta (PR 10).** Track the last completed assistant message of the last turn (the result tool's text or a plain final response); the loop's `lastText` is turn-local today (`agent/session_lifecycle.go:2176`, `:2330`). Carry it on the envelope (`EvenerThread.LastMessage *MessageExcerpt{text, at}`), and persist it in `SessionMeta` at turn end so ended sessions have it without a transcript read.
- **Hub (PR 11).** Live rows from the probe, ended rows from the meta; the summary gains `last_message` (at most 200 runes, cut at a word boundary on the daemon side).
- **Fallback.** Generic copy.
- **Tests.** Agent: excerpt after a clean turn, updated by the next, persisted in meta, survives restore. Hub: probe, past-index meta, navigation, schema, codec.
- **Open question.** Byte budget: 2,000 rows times 200 runes is about 400 KB, inside the 2 MiB response cap (`maxNavigationResponseBytes`) but worth measuring with `fitNavigationSection`. If it crowds the fitter, carry the excerpt only on live rows.

### Documents and artifacts named in the final message

Blocked on the shared-artifacts work (not on main). No PR here; the phone shows no attachment chips until it lands.

---

## PR 12: Task progress for live local sessions (S13a, task level)

**Adds.** The row's task line, "☑ Task 4 of 7 · Fix the settle/drain race" (spec 7.2). The probe already receives `root.Evener.Tasks` every five seconds and drops it (`prober.go:148-165`).

**Files and changes:**
- `hubcore/prober.go` and `roster.go`: `ProbeResult.Tasks` and `LiveEntry.Tasks *appwire.TaskAggregate` from `root.Evener.Tasks`, deep-copied with `appwire.CloneTaskAggregate` (export the existing `cloneTaskAggregate`, `appwire/clone.go:135`; `server/appwire_runtime.go:992` holds a second copy of it that switches to the export in the same PR); copy in `liveEntryFromProbe`, `cloneLiveEntry` and `cloneNavigationLiveEntries`; hash total, done, cancelled, the current task's ID and description in `rosterFingerprint`, or a task change never invalidates navigation.
- `hubcore/tree.go`: `TreeNode.Tasks *appwire.TaskAggregate` from a `tasksFor(id)` closure in the three builders; deep copy in `cloneTreeNodesContext`.
- `hubapi/navigation.go`:

```go
// NavigationTaskProgress is a live session's task-list progress: how many of
// its tasks exist, are done and were cancelled, and the first task in
// progress. Carried only when the list is non-empty.
type NavigationTaskProgress struct {
	Total     int    `json:"total"`
	Done      int    `json:"done"`
	Cancelled int    `json:"cancelled,omitempty"`
	CurrentID int    `json:"current_id,omitempty"`
	Current   string `json:"current,omitempty"`
}
```

  and on the summary `Tasks *NavigationTaskProgress` with JSON `tasks,omitempty`, set by `projectShallow` only when `Total > 0`, with `Current` truncated to `maxNavigationLabelRunes`. Deep copy in `cloneNavigationSummary`.
- `navigation_schema.go`: counts non-negative, `Done + Cancelled <= Total`, `Current` at most 512 runes. `codec.ts`: `TASKS_KEYS` nested under `SESSION_KEYS` and a `tasksValue` validator with the same bounds. Fixture: a `tasks` record. `make generate`.

**Tests:** a prober test where `wireProbeEnvelopeSource.TaskAggregate()` (`prober_wire_test.go:134`, which returns nil today) returns an aggregate the probe keeps; `TestRosterRefreshFiresOnChangeWhenTasksMove` beside `roster_test.go:701`; a hubcore scenario for all three builders; a navigation test (present with a list, absent without); schema bounds; codec keep and refuse; the fixture guard.

---

## S13b: Task progress for remote-host sessions (PR 13, design level)

- **Adds.** The task line on sessions running on another host.
- **Build points.** The PR 5 pattern: `LocalDaemonEntry.Tasks`, `threadFromEntry` emits `Evener.Tasks`, `appThreadTreeEntries` reads it.
- **Fallback.** No task line on remote rows.
- **Tests.** A `threadFromEntry` test, `appThreadTreeEntries` carries it, a remote-row navigation test.
- **Open questions.** Ended sessions stay without a task line: `persistedTaskAggregate` costs a disk read per session (`app_threadread.go:945-965`), and a Board row for an ended session is a quiet one-line row anyway.

---

## S5: Activity pulse (PRs 14-15, design level)

- **Adds.** The pulse meter's seven one-minute bars ("transcript items and tool output events", spec 16.4) and the last activity time behind "Quiet 4m" (3 to 10 minutes) and "May be stuck" (10 minutes or more).
- **Why not on the row.** Buckets change every minute. On the revisioned navigation resource they would move the fingerprint on every probe, bump revisions and broadcast invalidations about once a minute per working session (`navigation_service.go:957-1064`), and `NextBoundary` has no minute schedule (`:1370-1399`).
- **Daemon (PR 14).** A per-root counter in the server, incremented from `RecordAppEvent` (`server/appwire_runtime.go:413`) and `RecordDescendantAppEvent` (`:692`) for item events and tool-output events. It is a plain increment outside the envelope, which must not sample on deltas (`server/thread_envelope.go:166-176`). A ring of one-minute buckets on the server's clock, plus the last event time. The StatusOnly root row carries `EvenerThread.Activity *ThreadActivity{minutes []int (oldest first, seven entries, the current minute last), lastActivityAt int64 (ms)}`; deep copy in `appwire/clone.go`.
- **Hub (PR 15).** The probe keeps it on `LiveEntry`, deliberately left out of `rosterFingerprint`. A new method `evener/activity/read` returns `{sessions: [{ref, minutes, lastActivityAt}]}` for live top-level sessions; the phone polls it while the Board is on screen. Catalog row, handler, `TestHubRouterMatchesCatalog`, `TestHubRPCRegistersExpectedHandlerSet`.
- **Fallback.** `updated_at`, and a single bar.
- **Tests.** Server counter with an injected clock (minute rollover, a quiet minute is zero, descendants counted or not per the ruling); the StatusOnly row carries it; the prober decodes it; the fingerprint does not move when only activity moves; the method handler, including remote rows.
- **Open questions.** Whether a coordinator's meter and stuck timer count its subagents (Questions for Jesse). Poll or push: polling keeps the cost with the viewer; a push would be one broadcast per probe tick per client. Remote hosts: fan the read out to attached hosts, or carry `Activity` on the remote hub's list rows (they refresh through `RemoteThreadCache` on their own cadence).

---

## S4: Seen-through marker (PRs 16-17, design level)

- **Adds.** A per-session "seen through" marker shared by the phone and the web, so Finished (blue dot) and Idle agree across devices (spec 7.1: "A blue dot marks the ones you haven't opened since").
- **Daemon (PR 16).** "Finished" needs the time the last turn ended; `SessionMeta.UpdatedAt` also moves on writes that are not turns. Stamp `EvenerThread.LastTurnEndedAt int64` (ms) at `EventTurnEnded`, on the StatusOnly row and in `SessionMeta`. The summary gains `turn_ended_at *time.Time` with JSON `turn_ended_at,omitempty`.
- **Hub (PR 17).** A `SeenStore` in `index.db` beside the archive, favorite and pin stores, modeled on `PinSectionStore` (`hubcore/pin_section.go:48-68`): rows `(source_id, session_id, seen_through_ms)`. Method `evener/session/seen/set` with params `{refs: string[], seenThrough?: int64}` (an absent `seenThrough` clears, which is "Mark as unread"), returning a `NavigationMutation` receipt like `evener/archive/set` (`app_archive.go:14-18`); the store's `SetOnChange` invalidates navigation (`main.go:631-646`). A decoration map in `navigationBuildInputs` (`navigation_projection.go:94-114`), assembled in `navigationBuildInputsFromTreeSnapshot` (`web_api_tree.go:233`); the summary gains `seen_through *time.Time`. A row is unseen-finished when `turn_ended_at` is after `seen_through`.
- **Fallback.** A phone-local marker.
- **Tests.** Store (set, clear, read, reopen), handler, catalog tests, the navigation test, invalidation on change, the daemon stamp and its restore, the prober.
- **Open questions.** "Per-user" is per hub: the hub has no user identity (`initialize` carries only `clientInfo.name`), which is the same thing on a personal hub. The web marks a session seen when it opens one, so the phone's dot clears; whether the web also shows dots is a web design question for later. Clustering: `clusterable` (`tree.go:1808-1816`) folds same-titled idle or ended rows that have no children, jobs or watches. Most finished turns rest `awaiting` and never fold, but a turn that ended with no output rests `idle`, so an unseen one could vanish into a cluster; the marker has to reach `BuildTree` so `clusterable` can skip unseen rows.

---

## S3: Subagent tallies (PRs 18-19, design level)

- **Adds.** Per top-level session: running, failed and done subagents over the whole tree, omitted descendants included, "as the hub's job counts are (active, failed, completed)" (spec 9). It drives the row's "2 subagents failed" and the Subagents chip's strip on 500-node trees.
- **Daemon (PR 18).** The hub cannot count failures of ended subagents: `SessionMeta` has no outcome, the StatusOnly probe drops delegate outcomes, and a root's delegate list holds only its direct children (explorer report section 7). The daemon already aggregates `JobActivityCounts` over the delegate tree for `evener/jobs/list` (`aggregateActivity`, `agent/jobs_activity.go:1519-1571`). Put the same type on the StatusOnly root row: `EvenerThread.SubagentCounts *JobActivityCounts`, memoized by the session's job-tree revision (`SessionMeta.JobTreeRevision`) so the five-second probe does not re-walk an unchanged tree.
- **Hub (PR 19).** The probe keeps it (clone, fingerprint); `TreeNode`; the summary gains `subagents *NavigationSubagentCounts{running, failed, done, complete}`; schema bounds, codec, fixture. The counts move on subagent lifecycle events, so navigation revisions follow real events and never a clock.
- **Fallback.** Tally the loaded children; "+N more".
- **Tests.** Daemon aggregation over a nested tree with failed, done and running delegates past the per-parent cap; the StatusOnly row; memoization (an unchanged revision does not re-walk); prober, fingerprint, navigation, schema, codec.
- **Open questions.** The cost of `aggregateActivity` on a 500-node tree, to be measured before choosing memoization over a daemon-side cache.

---

## S12: Scoped approvals (PRs 20-21, design level)

- **Adds.** "Allow all of ~/sites/docs · For the rest of this session" on the approval dock (spec 8.4), so a batch job does not ask 214 times.
- **Today.** Approval re-runs the one denied call with a grant carried on the context and a throwaway environment clone (`agent/session_escalation.go:216`; `agent/session_tools.go:923-935`; `agent/execenv/local.go:457-478`). No session-scoped grant exists; `isGranted` is exact equality (`agent/execenv/securepath.go:116-120`); nothing persists across a restart.
- **Daemon (PR 20).** A session grant list of `(folder, access)` consulted at the two containment checks (`agent/execenv/securepath_fdops_unix.go:91-105` for reads, `:231-245` for writes) as a `containingRoot`-style prefix check (`securepath.go:379`), for the access kind the escalation was for. The folder is the denied path's parent directory. Refuse a scope that is the filesystem root or the home directory itself, and never cover a sensitive path (those never escalate).
- **Wire and hub (PR 21).** `SandboxEscalationRequested` gains `ScopeFolder string` with JSON `scopeFolder,omitempty`: the folder the daemon would grant. Its presence is the capability; an older daemon omits it and the phone offers "Allow once" only. `SandboxEscalationResolveParams` gains `Scope string` with JSON `scope,omitempty` (`""` is today's once, `"folder"` the folder). The hub passes it through (`app_rpc.go:1634-1645`, `local_daemon.go:466-474`).
- **Fallback.** Allow once, repeatedly.
- **Tests.** A folder grant auto-allows a later write under the folder without raising an escalation, and still escalates outside it or for the other access kind; a deny or an interrupt grants nothing; root and home are refused as scopes; the server handler passes the scope; `make generate`.
- **Open questions.** Whether the grant survives a daemon restart (Questions for Jesse). The non-Unix securepath files need the same check for parity.

---

## S11: Hub notices feed (PRs 22-23, design level)

- **Adds.** The Board's notices (spec 7.1): a provider sign-in expired, a host offline, a plugin broken, each naming its affected sessions and one action.
- **What the hub knows today.**
  - Sign-in: `AuthStatusResponse` has `SignedIn`, `NeedsRefresh`, `NeedsLogin` and an `Error` that nothing fills (`appwire/types.go:2401-2440`). Only Codex OAuth instances compute it, from the access token's expiry (`cmd/evener-hub/app_auth.go:1020-1039`), so `NeedsLogin` marks a record the daemon would refresh silently as expired. The expiry itself is dropped from the wire (`:1677-1694`). A real "sign in again" happens only at runtime in the daemon (`ErrLoginRequired`, `llm/providers/tokenauth/codex.go:55-58`), and the hub never records it. No refresh-token lifetime exists anywhere, so "expiring within a day" cannot be computed. `evener/auth/updated` fires on mutations only.
  - Host offline: the manifest's `sources[].online` (`web_api_tree.go:928-951`) and `HostRow.LastAttachErr` (`appwire/types.go:4150-4169`); no "offline since" time.
  - Plugin broken: `PluginEntry.Broken` is a bool with no reason (`appwire/types.go:3781`, `internal/plugins/install.go:409`).
  - Affected counts: by host, yes (rows carry `host_id`); by provider, only through `LiveEntry.Provider`, which goes stale after a model switch (the probe drops `Evener.Profile`); by plugin, no (plugins are stripped from the StatusOnly row).
- **Design (PR 22).** Derive notices in the hub from facts it can stand behind: a sign-in notice from live sessions resting Failed whose failure cause is an auth failure (S1c's `cause_kind` and `provider`), grouped by provider instance; a host notice from offline sources, counting that host's live rows; a plugin notice from broken plugins, without a count until the probe carries enabled plugins. New method `evener/notices/list` returning `{notices: [{id, kind: "signInExpired" | "hostOffline" | "pluginBroken", subject, label, affectedSessions?}]}`, and a payload-free notification `evener/notices/changed` fired when the derived set changes (the TUI lists it in `notifyMethodsDeliberatelyIgnored`). A separate method keeps notices off the navigation revision, since they change on their own clock.
- **Sign-in state (PR 23).** Stop `NeedsLogin` flagging a refreshable token as expired, and record the daemon's runtime login-required failures where `evener/auth/list` can report them. Spec 12's "Expires in 3d" on the Hub sheet needs the same fix; tell the phone lane (phase 5) it depends on this PR.
- **Fallback.** Derive from `evener/host/*` and `evener/auth/list` reads.
- **Tests.** The derivation over fixtures (an auth-failed session, an offline host, a broken plugin), the handler, the notification firing once per change, catalog tests, the TUI coverage list.
- **Open questions.** Dropping "expiring within a day" (Questions for Jesse). File a GitHub issue for the `NeedsLogin` misreport whichever way that goes.

---

## S14: Message-text search (PRs 24-25, design level)

- **Adds.** Search's "In sessions" group (message-text hits with a highlighted snippet, and the hit's position to scroll to) and its Archived scope (spec 7.4).
- **Where message text is indexed today: nowhere.** `hubSearch` (`cmd/evener-hub/app_search.go:14-54`) matches live sessions by ID or title substring and past sessions through `PastIndex.Search` (`hubcore/past.go:578-604`), whose FTS5 table `past_sessions_fts` holds only ID, name, original prompt and working directory (`past.go:655-664`). The transcript sidecar index (`internal/apptranscript/turn_index.go:686-697`) keeps offsets, lengths and kinds and deliberately no text. The agent's `find_session_transcripts` tool scans the 200 newest transcripts per query (`agent/session_tools_find.go:26-37`) and cannot be called from the hub. `SearchResult` is `{id, title, project, state, age, ref}` (`appwire/types.go:580-603`): no snippet, no archived flag. Search does not fan out to remote hosts.
- **What it costs.** Transcripts are append-only JSONL at `<project state>/sessions/<ID>.transcript.jsonl` with user images inline. On the machine this plan was written on: 1,105 transcripts, 512 MB, median 0.17 MB, p90 0.7 MB, p99 3.5 MB, largest 53 MB. Scanning per query means reading and decoding about half a gigabyte per keystroke, which rules it out. An FTS5 index of message text costs roughly the text's size on disk (images and tool output left out) and one full read to build; after that, append-only files allow a per-file byte high-water mark, updated from the past index's periodic rebuild (`cmd/evener-hub/config.go:59`, `cmd/evener-hub/main.go:687`). The SQLite driver already has FTS5 (`past.go:19`), and FTS5's `snippet()` makes the snippets. `index.db` is shared with the archive, favorite and pin stores and already handles `SQLITE_BUSY` (`past.go:94-101`), so the text index gets its own file.
- **Design.** PR 24: the index (`<hub state>/search.db`), rows keyed by session and transcript entry, holding user and agent message text. PR 25: `SearchParams.scope` (`"all" | "live" | "archived"`); `SearchResult.archived bool`, computed by an exported helper that applies the rail's rules (explicit decisions plus the 14-day auto-archive, `tree.go:398-414`, today unexported in `decisionFor` and `classifySession`); `SearchResult.hits [{snippet, itemKey}]`, where `itemKey` is the transcript item the phone scrolls to (the key its reader position already uses); fan-out to attached hosts, which adds `evener/search` to the host proxy allow-list (`app_host_admin.go:41-136`).
- **Fallback.** Sessions and Projects groups only, with the All and Live scopes.
- **Tests.** Index build and incremental update over `hubtest` fixtures (appending to a transcript indexes only the new entries); the handler (snippet, archived flag, each scope); remote fan-out with a scripted source.
- **Open questions.** Mapping an index row to an item key: store the entry sequence and map it through the turn-index sidecar at query time, or store the key at index time. Index size on a busier hub.

---

## S7: Remote document and image proxying (PRs 26-27, design level)

- **Adds.** Plans, documents and images in a session on another host render on the phone.
- **Today.** The controller reaches a host through `ssh ... hub attach --stdio`, which carries AppWire messages only (`cmd/evener-hub/internal/sshconn/doc.go:6-14`, `cmd/evener-hub/attach.go:25-35`). `/doc/file` takes `session`, `path` and `format` and refuses a host-qualified ref with a 404 (`doc_serve.go:43-44`, `web.go:349-355`). Remote image URLs are blanked on purpose (`stripRemoteImageRoutes`, `app_rpc.go:220-281`). The web opens a doc pane for a remote session that shows "File not available" (`fileOpenBeside.tsx:55-60`).
- **Design.** PR 26: read-only methods on every hub, `evener/doc/read` with params `{ref, path}` returning `{data (base64), mediaType, truncated, totalSize, revision}` (512 KiB cap, the same path confinement as `/doc/file`), and `evener/image/read` with params `{ref, sha}` (images up to 8 MiB, about 11 MB base64, inside the 128 MiB message cap). PR 27: the controller's `/doc/file`, `/doc/image` and `/s/<ref>/images/<sha>` forward a host ref over the attached client; proxy URLs replace the blanking; both methods go on the host proxy allow-list; the web drops its local-only check.
- **Fallback.** An "Open on the host" notice.
- **Tests.** Host-side handlers (confinement, cap, truncation), controller forwarding with a scripted remote source, the web's remote doc pane.
- **Open questions.** A host on an older build lacks the methods; sshconn redeploys a host whose version differs (`internal/sshconn/doc.go:17-19`), so a stale host answers "method not found" until then and the fallback holds.

---

## S9: Document revision identity (PR 28, design level)

- **Adds.** "3 changes since you read it yesterday" in the Reader (spec 10.2).
- **Today.** `/doc/file` sends no `ETag` or `Last-Modified` and ignores `If-None-Match` (`doc_serve.go:38-79`, `:208-219`). `/doc/image` sends a sha256 `ETag` but never answers 304 (`:123-126`). `DocFileContent` has no revision, and `DocFetch` takes only a URL (`appwire-client/typescript/docContent.ts:16-26`, `:66`).
- **Design.** `/doc/file` sends a weak `ETag` from size, modification time and a hash of the served bytes, plus `Last-Modified`, and answers `If-None-Match` with 304. A hash of the served 512 KiB head alone would miss changes past the cap, so size and time ride with it. `DocFileContent` gains `revision` and `modifiedAt`; `DocFetch` accepts request headers.
- **Fallback.** Diff against the phone's cached copy.
- **Tests.** `doc_serve_test.go` (stable for an unchanged file, changed after an edit, 304 on a match, 200 after a change); `docContent.test.ts` (revision parsed, `If-None-Match` sent).
- **Open questions.** None that change behavior.

---

## S8: Hub-stored launch recipes (PR 29, design level)

- **Adds.** Recipes that follow you across devices and appear on the web: host, project, model, effort, plugins, access and branch (spec 11), with list, edit, reorder and delete (spec 12).
- **Today.** Nothing exists. The web keeps per-directory spawn defaults in `localStorage` (`frontend/src/panes/spawn/spawnDefaults.ts:13-32`, `:145-204`); launch configuration layers are TOML files behind `evener/launch/*` (`internal/launchconfig/paths.go`).
- **Design.** A `LaunchRecipeStore` modeled on `KeybindingsStore` (a JSON file with a revision, `hubcore/keybindings_store.go:20-65`): methods `evener/launch/recipes/get` and `evener/launch/recipes/patch` (with `ExpectedRevision`), notification `evener/launch/recipes/changed`, which the TUI lists as deliberately ignored. A recipe is `{id, name, sourceId, cwd, model, effort, enabledPlugins, access, network, branch, order}`. Recipes live on the controller hub and name their host, so no remote fan-out is needed.
- **Fallback.** Phone-local recipes.
- **Tests.** Store (create, patch, reorder, revision conflict), handlers, catalog tests, the TUI coverage list.
- **Open questions.** "Same as last time" (the last setup used for the chosen project) could come from the hub's newest session meta in that project, with no store at all, so web and phone agree for free; decide at planning time. The web's spawn sheet adopting recipes is web work outside this lane.

---

## S6: Direct subagent stop (PRs 30-31, design level)

- **Adds.** "Stop subagent" with a confirmation (spec 9) in place of "Ask coordinator to stop it".
- **Today.** A subagent's thread is a read-only alias: the hub refuses mutations on it (`entryForRef` skips aliases, `internal/appsource/local_daemon.go:1048-1068`), the daemon refuses any non-root target (`requireRootMutationTarget`, `server/appwire_runtime.go:2319-2386`), and aliases advertise no capabilities. The only delegate stop is the model's `job_stop` tool, which calls `delegateController.StopSubtreeAndDrive` (`agent/delegate_tree_stop.go:106-179`): durable (`EventDelegateSubtreeStopRequested`), always the whole subtree. Authorization lets the root actor stop a top-level delegate and only a delegate's parent stop a nested one (`agent/delegate_tree_controller.go:360-381`).
- **Design.** PR 30: a daemon method `evener/delegate/stop` with params `{ref (the root), delegateId, clientMutationId}`, retry-safe like `turn/interrupt` (`lockRetrySafeMutation`), running `StopSubtreeAndDrive` as a new human actor authorized for any delegate in the root's tree, and added to the retirement and recovery admission switches (`server/appwire_retirement_admission.go:19-36`, `cmd/evener-hub/app_sources.go:328-363`). PR 31: hub routing by the root ref, a `ThreadCapabilities.StopSubagent` bit on the root thread, and the catalog. The phone maps a subagent row to its delegate ID through `EvenerDelegateInfo` (`appwire/types.go:1233-1240`).
- **Fallback.** Steer the coordinator.
- **Tests.** Agent: a human stop of a nested delegate stops its subtree and records the durable event. Server: root target, an unknown delegate refused, a retry replays the receipt. Hub routing, catalog tests. No new notification: `evener/delegate/updated` already reports the result.
- **Open questions.** Stopping any subagent, and the subtree semantics (Questions for Jesse). A direct message to a subagent is not planned: the spec's subagent screen routes talk through the coordinator.

---

## Questions for Jesse

1. Should a coordinator's pulse meter and "may be stuck" timer count its subagents' activity? I recommend yes: a coordinator waiting on 31 working subagents is busy.
2. The hub cannot know a sign-in is "expiring within a day" (no refresh-token lifetime exists). May S11 drop that and show "sign-in expired" only when a session actually failed on it? I recommend yes.
3. Should a scoped approval ("allow all of this folder for the rest of this session") survive a daemon restart? I recommend no: a restart asks again, so a folder grant never outlives the process that was granted it.
4. May the phone's "Stop subagent" stop any subagent, including one another subagent started, and does it stop that subagent's whole subtree? I recommend yes to both: it is how the tree's own stop works, and you own the whole tree.

No planned wire change is non-additive. PR 2 reuses the existing `systemError` status value, so `ProtocolVersion` stays `evener-appwire-v5`.

---

## Self-review

- **Spec coverage.** S2 (PRs 3, 4, 5), S1 (PR 6, S1b-S1d, the blocked documents part), S13 (PRs 12, 13), S5, S4, S3, S12, S11, S14, S7, S9, S8, S6 each have a section with the fallback from spec 18. S10 is out of scope per the roadmap. Rulings 1 to 4 map to PR 1, PR 2, Global Constraints and the PR map.
- **Explorer report against `8cc794480`.** Its citations for `hubcore`, navigation, the codec and `appwire` hold (only `internal/sshconn` and `mobile-native/src/design/tokens.ts` changed since its base `73897272e`). One citation moved: the `MobileAPIVersion` check is now `internal/sshconn/version.go:1460`, not `:1390`. Its note that no AGENTS.md or docs rule requires a TUI case for projector changes is accurate; the rule this plan applies comes from the lane's own gate list.
- **Placeholders.** PRs 1 to 3 carry their code. PR 4 and later name files, interfaces and tests and leave code for their own plans, as the lane's detail levels ask.
- **Names.** `ValueRecordKeys`, `knownKeys`, `dropUnknownKeys`, `SESSION_KEYS`; `RestingWireState`, `historyEndsInTurnFailure`; `IsRestingThreadStatus` and `isSessionResting`; `ApprovalPending` (`approval_pending`, `approvalPending`); `ApprovalTool` and `ApprovalTarget`; `NavigationTaskProgress`. Each is used the same way wherever it appears.
- **Review Focus.** Item 1: Task 1.2. Item 2: Task 2.1's projector and server tests and Task 2.2's retargeted live-subscriber test. Item 3: `TestWireState_InterruptIsNotAFailedTurn`. Item 4: `TestRestore_FailedTurnResumesFailed` and `TestServe_FailedTurnReportsSystemErrorAcrossRestart`. Item 5: Task 2.3's appwire-client, web and TUI tests.
