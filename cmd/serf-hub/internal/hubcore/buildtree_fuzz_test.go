package hubcore

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
)

// fixedTreeClock is an injected wall clock so tier classification
// (Current/Recent/Archived) is deterministic across fuzz replays.
var fixedTreeClock = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// FuzzBuildTree drives the real hubcore.BuildTreeAt seam, the sidebar projection
// core. It decodes fuzzed JSON into the three inputs BuildTreeAt consumes —
// []schema.SessionMeta (the persisted sessions), []LiveEntry (the live daemons),
// and the archive decisions map — and projects them into the navigation Tree.
// This exercises project grouping, fork/subagent nesting, tier classification,
// repeated-title clustering, rollups, and all the ordering comparators. The
// oracle is floor "no panic" plus re-serializability of the resulting Tree
// (it is rendered to the web client, so it must marshal cleanly).
func FuzzBuildTree(f *testing.F) {
	f.Add(
		[]byte(`[{"id":"s1","env_info":{"working_dir":"/a"},"created_at":"2025-12-31T00:00:00Z","updated_at":"2025-12-31T12:00:00Z"}]`),
		[]byte(`[{"SessionID":"s1","Status":"awaiting"}]`),
		[]byte(`{}`),
	)
	f.Add(
		[]byte(`[{"id":"p","env_info":{"working_dir":"/a"}},{"id":"c","is_subagent":true,"parent_session_id":"p","env_info":{"working_dir":"/a"}}]`),
		[]byte(`[{"SessionID":"p","Status":"active"},{"SessionID":"c","Status":"active"}]`),
		[]byte(`{}`),
	)
	f.Add(
		[]byte(`[{"id":"orig","fork_label":"old","env_info":{"working_dir":"/a"}},{"id":"new","parent_session_id":"orig","env_info":{"working_dir":"/a"}}]`),
		[]byte(`[]`),
		[]byte(`{}`),
	)
	f.Add([]byte(`[]`), []byte(`[]`), []byte(`{}`))
	f.Add([]byte(`not json`), []byte(`not json`), []byte(`not json`))

	f.Fuzz(func(t *testing.T, metasRaw, liveRaw, decisionsRaw []byte) {
		var metas []schema.SessionMeta
		if err := json.Unmarshal(metasRaw, &metas); err != nil {
			return // rejected input
		}
		var live []LiveEntry
		if err := json.Unmarshal(liveRaw, &live); err != nil {
			return
		}
		// Decisions arrive as a flat {"session:id":bool,"project:name":bool} map so
		// the fuzzer can drive archive/unarchive overrides without an exotic key type.
		var flat map[string]bool
		if err := json.Unmarshal(decisionsRaw, &flat); err != nil {
			return
		}
		decisions := make(map[ArchiveKey]bool, len(flat))
		for k, v := range flat {
			kind, id := "session", k
			if k != "" {
				if cut := splitKindID(k); cut.kind != "" {
					kind, id = cut.kind, cut.id
				}
			}
			decisions[ArchiveKey{Kind: kind, ID: id}] = v
		}

		tree := BuildTreeAt(metas, live, decisions, fixedTreeClock)
		if _, err := json.Marshal(tree); err != nil {
			t.Fatalf("BuildTreeAt result failed to marshal: %v\n metas=%q\n live=%q", err, metasRaw, liveRaw)
		}
	})
}

type kindID struct{ kind, id string }

// splitKindID parses a "kind:id" decision key into its parts; an unprefixed key
// leaves kind empty so the caller defaults it to "session".
func splitKindID(k string) kindID {
	for i := 0; i < len(k); i++ {
		if k[i] == ':' {
			prefix := k[:i]
			if prefix == "session" || prefix == "project" {
				return kindID{kind: prefix, id: k[i+1:]}
			}
			return kindID{}
		}
	}
	return kindID{}
}
