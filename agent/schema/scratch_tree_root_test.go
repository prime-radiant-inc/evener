package schema

import "testing"

// RestoredScratchTreeRoot names the tree a session's scratch lives in: the
// session itself for a root session, the recorded tree root for a delegate,
// and a delegate resumed on its own keeps the tree of the root that spawned
// it. Callers that reconstruct a scratch directory from meta depend on exactly
// this answer.
func TestSessionMeta_RestoredScratchTreeRoot(t *testing.T) {
	t.Parallel()
	const root = "02wMz5Txv1C3Hut0M8GCeB"
	const otherRoot = "02wMz5Txv1C3Hut0M8GCeF"
	const delegate = "02wMz5Txv1C3Hut0M8GCeC"
	cases := []struct {
		name string
		meta SessionMeta
		want string
	}{
		{
			name: "a root session owns its own tree",
			meta: SessionMeta{ID: root, ScratchTreeRootID: otherRoot},
			want: root,
		},
		{
			name: "a delegate keeps the recorded tree root",
			meta: SessionMeta{ID: delegate, IsSubagent: true, ScratchTreeRootID: root},
			want: root,
		},
		{
			name: "a resumed delegate falls back to the job tree root",
			meta: SessionMeta{ID: delegate, IsSubagent: true, JobTreeRootSessionID: root},
			want: root,
		},
		{
			name: "an unparsable tree root id is ignored",
			meta: SessionMeta{ID: delegate, IsSubagent: true, ScratchTreeRootID: "not a session id"},
			want: delegate,
		},
		{
			name: "a blank tree root on a delegate",
			meta: SessionMeta{ID: delegate, IsSubagent: true},
			want: delegate,
		},
	}
	for _, tc := range cases {
		if got := tc.meta.RestoredScratchTreeRoot(); got != tc.want {
			t.Errorf("%s: RestoredScratchTreeRoot() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
