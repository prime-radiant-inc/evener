# Session Tree History Assessment

Status: Optional assessment / decision document. This is not a committed feature spec. We are explicitly unsure that Serf needs a tree-native history API now; the preferred outcome may be to keep the current fork lineage metadata and defer any new API until a Hub/TUI/CLI/SDK consumer needs it.

> **Evergreen pointer:** the shipped lineage baseline this assessment inventories (fork vs subagent metadata, `ParentSessionID`/`DivergenceTurn`/parent-stored `ForkLabel`, `find_session_transcripts(children_of)` with its `kind` discriminator) is documented as current reality in [`../subagent-runtime-contracts.md`](../subagent-runtime-contracts.md) ("History and lineage"). The session-tree API remains deferred per this doc's Option A.

## Purpose

Assess whether Serf should expose a first-class tree/history view over forked sessions, and, only if the answer is yes, define the smallest read-only API and implementation plan that can support that view without adding premature storage, indexing, or transcript rewrites.

The current implementation already records enough lineage to reconstruct many fork relationships:

- `ForkSession` creates a child transcript from a parent prefix and an edited user turn; `divergenceTurn` is a 1-based index into the parent's full transcript entry list, not a user-input ordinal (`agent/fork.go:19-29`, `agent/fork.go:62-116`, `agent/fork.go:126-167`).
- The child transcript header records `ParentSessionID` (`agent/fork.go:126-141`).
- Child session metadata records `ParentSessionID`, `DivergenceTurn`, and an empty child `ForkLabel` (`agent/fork.go:173-187`).
- The parent metadata may be updated with `ForkLabel` (`agent/fork.go:193-199`).
- `SessionMeta` defines `ParentSessionID`, `DivergenceTurn`, and `ForkLabel` as fork lineage/display fields, while the same `ParentSessionID` relation is also used by subagent metadata and must be classified with `IsSubagent` (`agent/schema/snapshot.go:38-49`).
- `ListSessionMetas` already scans all `*.meta.json` files and returns valid session metadata (`agent/schema/snapshot.go:144-174`).
- Transcript headers include `ParentSessionID` and `ParentToolCallID` fields, currently documented as subagent parentage fields (`agent/transcript/transcript.go:27-51`); fork code also sets `ParentSessionID` and can copy an existing `ParentToolCallID` when forking a subagent transcript, so any future tree view must prefer metadata-derived relation type over header fields when classifying fork lineage versus subagent spawn lineage.
- `find_session_transcripts(children_of=...)` is already a public lineage discovery surface that returns direct children (subagents and forks) for a transcript ref using metadata, scoped to that ref's project. It is a flat direct-child query, not a nested tree API, and current results are gated on readable child transcripts.

## Goals

- Decide whether tree-native history is needed at all.
- Keep the current metadata-first fork model as the baseline.
- If implemented, provide a derived, read-only tree/forest query over existing session metadata.
- Represent multiple children from one parent without losing information.
- Handle missing/deleted parents as broken references or orphaned nodes, not fatal errors.
- Preserve fork lineage across resume/meta rewrites.
- Keep user-visible fork history separate from subagent spawn history by default.
- Avoid new persistent child indexes unless measurement proves scanning session metadata is too slow.

## Non-goals

- No automatic commitment to tree-native history.
- No DAG history model; fork history is parent-session tree/forest lineage.
- No transcript de-duplication, content-addressed storage, branch merge, or replay engine.
- No rewriting existing transcripts or metadata as part of a read-only tree query.
- No persistent child index in the first implementation.
- No inclusion of subagent trees by default.
- No UI mandate for Hub/TUI/CLI; this doc only defines an optional data surface if a consumer needs it.

## Assessment questions before implementation

Do not implement the API below until these questions have affirmative, concrete answers:

1. Which consumer needs this now: Hub, TUI, CLI, SDK, tests, or none?
2. Can that consumer reconstruct the needed view from `find_session_transcripts(children_of=...)`, `ListSessionMetas`, and existing `SessionMeta` fields?
3. Is the current parent-stored `ForkLabel` semantics correct for multiple forks from one parent?
4. Does the consumer need a nested tree, or would a flat list with `ParentSessionID` and `DivergenceTurn` be enough?
5. Should missing parents appear as root-level orphan nodes, attached placeholder parents, or diagnostics?
6. Should tree queries include only user-visible forks and exclude `IsSubagent` sessions by default?
7. Are transcript header `ParentSessionID` and session metadata `ParentSessionID` always expected to agree for forks after resume and metadata rewrite?
8. Are summaries/previews needed, or should the query return only IDs and metadata?
9. Is session count high enough that scanning all metadata is too expensive? If not measured, assume scanning is acceptable.

## Decision options

### Option A: no new feature now

Recommended if no immediate UI/API consumer needs tree history.

Behavior:

- Keep existing `ParentSessionID`, `DivergenceTurn`, `ForkLabel`, and `IsSubagent` fields.
- Rely on `find_session_transcripts(children_of=...)` for direct-child audit pivots and `ListSessionMetas` for any ad hoc nested view.
- Add or keep tests around lineage preservation and multiple-fork semantics only when needed.
- Document known ambiguity around parent-level `ForkLabel`.

This option is valid and should not be treated as a failure to implement.

### Option B: derived read-only tree query, no new storage

Maximum recommended near-term scope if a real consumer needs tree history.

Behavior:

- Scan session metadata using the existing metadata listing path.
- Build a forest in memory by connecting fork nodes whose `ParentSessionID` matches another known session ID. Keep relation type explicit when subagents are included.
- Exclude `IsSubagent` sessions by default unless the caller explicitly asks for them.
- Mark missing parent references instead of returning an error for the whole query. Distinguish a parent truly missing on disk from a parent hidden by filters, or define that descendants of excluded subagents are excluded too.
- Sort roots and children deterministically.
- Return lineage/display metadata only; do not read full transcript bodies. Decide whether the optional tree API is metadata-complete or readable-ref-compatible with `find_session_transcripts(children_of=...)`, which currently excludes children without readable transcripts.

### Option C: persistent child index

Not recommended now.

Only consider this if:

- measured session counts make metadata scanning too slow;
- Hub needs live incremental updates that cannot be handled by existing events/status refresh; or
- a future storage layer already maintains secondary indexes.

## Illustrative optional API if implemented

Prefer a narrow package-level query over new storage. Names and fields are a sketch until inclusion, ordering, orphan, filtering, readable-transcript, relation-type semantics, and alignment with existing Hub/transcript DTO vocabulary are chosen and tested. If this becomes a public API, prefer existing names such as `ref`/`session_id`, `title`, `kind`, `parent_ref`/`parent_session_id`, and `fork_label` unless a derived alias is explicitly documented.

```go
type SessionTreeOptions struct {
    IncludeSubagents bool
    IncludeOrphans   bool
    Sort             SessionTreeSort
}

type SessionTreeSort string

const (
    SessionTreeSortUpdatedDesc SessionTreeSort = "updated_desc"
    SessionTreeSortCreatedAsc  SessionTreeSort = "created_asc"
)

type SessionTreeNode struct {
    ID              string            `json:"id"`
    Name            string            `json:"name,omitempty"`
    CreatedAt       time.Time         `json:"created_at"`
    UpdatedAt       time.Time         `json:"updated_at"`
    ParentSessionID string            `json:"parent_session_id,omitempty"`
    DivergenceTurn  int               `json:"divergence_turn,omitempty"`
    Kind            string            `json:"kind"` // "root", "fork", or "subagent"
    BranchLabel     string            `json:"branch_label,omitempty"` // the node's own/original-branch label, not an edge label
    IsSubagent      bool              `json:"is_subagent,omitempty"`
    MissingParent   bool              `json:"missing_parent,omitempty"`
    HiddenParent    bool              `json:"hidden_parent,omitempty"`
    Children        []SessionTreeNode `json:"children,omitempty"`
}

func ListSessionTree(ctx context.Context, stateDir string, opts SessionTreeOptions) ([]SessionTreeNode, error)
```

If label semantics are changed to be edge-aware, add an edge DTO rather than overloading parent `ForkLabel`:

```go
type SessionTreeEdge struct {
    ParentID       string `json:"parent_id"`
    ChildID        string `json:"child_id"`
    DivergenceTurn int    `json:"divergence_turn"`
    Label          string `json:"label,omitempty"`
}
```

API rules:

- The query is read-only.
- The query must not open transcript files except possibly to repair/diagnose metadata/header disagreement in a separately requested validation mode.
- The query must not mutate session metadata.
- Missing/corrupt metadata files follow existing `ListSessionMetas` behavior unless a separate diagnostic mode is added.
- `Name` should use the existing `schema.SessionDisplayName(meta)` helper (`agent/schema/snapshot.go:89-100`).
- Children should be sorted deterministically; choose and test one default.
- Classify relation type from metadata before filtering: `IsSubagent` means a subagent/spawn edge; `!IsSubagent && (ParentSessionID != "" || DivergenceTurn > 0)` means a fork edge; sessions with neither are roots.
- If the API should match transcript-tool visibility, add a `RequireReadableTranscript` option or equivalent and document the difference from metadata-complete mode.

## ForkLabel assessment

Current implementation stores the supplied fork label on the parent metadata, not the child (`agent/fork.go:173-187`, `agent/fork.go:193-199`). Existing tests assert this behavior:

- child lineage and empty child label: `agent/fork_test.go:87-103`;
- parent label update: `agent/fork_test.go:105-112`;
- parent label preserved across metadata rewrite: `agent/fork_test.go:188-209`.

This may mean “label for the original branch before the fork,” not “label for the edge to the new child.” A `ForkLabel` field on a child node would therefore be misleading because current fork children normally have an empty label while the parent/original branch carries the display label. If users expect a label per fork, parent-level storage can be ambiguous or overwritten when multiple children are forked from the same parent. Do not change this casually. The assessment should pick one:

- keep parent `ForkLabel` as the original-branch display label, expose it as a node/branch label rather than an edge/child label, and document it clearly; or
- introduce child/edge label semantics in a separate migration-compatible spec.

## YAGNI/DRY implementation plan, only for Option B

1. Reuse `schema.ListSessionMetas(stateDir)`; do not create another metadata scanner.
2. Convert each `schema.SessionMeta` into a `SessionTreeNode` using existing fields and a derived `Kind`/relation type.
3. Build an ID map from all metadata first so filtered parents can be distinguished from missing parents.
4. Filter `meta.IsSubagent` unless `IncludeSubagents` is true, and either exclude descendants of hidden subagents or mark them with `HiddenParent` according to the chosen API behavior.
5. Append visible child IDs to the parent builder when `ParentSessionID` exists and the parent is present and visible, preserving each node's derived `Kind`/relation type. If `IncludeSubagents` is true, subagent children must be included as subagent edges rather than omitted or relabeled as forks.
6. For missing parents, either:
   - include the node as an orphan root with `MissingParent: true` when `IncludeOrphans` is true; or
   - omit/diagnose it according to the chosen API behavior.
7. Sort roots and each child slice with one shared comparator; do not duplicate sorting logic for roots and children.
8. Materialize `[]SessionTreeNode` values from the builder graph.
9. Keep validation/diagnostics separate from the read path. A normal tree query should not rewrite metadata, repair headers, or fail because a parent was deleted.
10. Do not add persistent indexes, caches, migrations, or transcript-body reads without a measured need.

Implementation should be small enough to live near existing session metadata listing code or an adjacent history/query package. Avoid a new framework.

## Acceptance criteria if implemented

- `ListSessionTree` returns a forest containing all root sessions and fork children visible under the selected options, with documented behavior for readable-transcript filtering versus metadata-complete results.
- Multiple children from the same parent are all represented.
- Missing parent references do not crash the query; they are represented deterministically as orphans/placeholders or omitted with diagnostics according to the final API choice.
- `ParentSessionID`, `DivergenceTurn`, derived relation kind, and the current node/branch label semantics are included in returned nodes.
- Subagents are excluded by default, or explicitly marked when included.
- The query is read-only and does not rewrite transcript or metadata files.
- Fork/resume flows preserve lineage fields already covered by existing tests.
- Sorting is deterministic and documented.
- The implementation reuses existing session metadata loading/listing code.

## Tests if implemented

- Build a three-level fork tree and verify nested structure.
- Create multiple children from the same parent and verify all appear.
- Delete or omit parent metadata and verify missing-parent/orphan behavior.
- Verify default filtering excludes `IsSubagent` sessions.
- Verify `IncludeSubagents` includes subagent nodes with `IsSubagent: true` when requested.
- Verify deterministic root and child sorting.
- Verify the query does not create, modify, or rewrite `*.meta.json` or `*.transcript.jsonl` files.
- Preserve existing fork tests:
  - `TestForkSession_CopiesPrefixAndAppliesEdit` (`agent/fork_test.go:68-156`);
  - `TestForkSession_ChildLineagePreservedAcrossMetaRewrite` (`agent/fork_test.go:158-186`);
  - `TestForkSession_ParentForkLabelPreservedAcrossMetaRewrite` (`agent/fork_test.go:188-209`);
  - out-of-range and missing-parent rejection tests (`agent/fork_test.go:211-238`).
- Add a characterization test for current multiple-fork `ForkLabel` behavior before changing label semantics.

## Recommendation

Default to Option A unless a concrete client needs tree-native history now. If a consumer does need it, implement only Option B: a derived, read-only forest query over existing metadata. Do not add persistent child indexes or edge storage until there is measured pressure or a clear UX requirement.
