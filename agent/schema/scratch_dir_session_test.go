package schema

import "testing"

// RestoredScratchDirSession names the session whose directory a session's
// scratch occupies: the recorded owner for a delegate that shares its
// parent's environment, the session itself in every other case. The hub
// serving a session's scratch depends on exactly this answer.
func TestSessionMeta_RestoredScratchDirSession(t *testing.T) {
	t.Parallel()
	const root = "02wMz5Txv1C3Hut0M8GCeB"
	const delegate = "02wMz5Txv1C3Hut0M8GCeC"
	const owner = "02wMz5Txv1C3Hut0M8GCeE"
	cases := []struct {
		name string
		meta SessionMeta
		want string
	}{
		{
			name: "a session that owns its scratch",
			meta: SessionMeta{ID: root},
			want: root,
		},
		{
			name: "a shared-environment delegate resolves through the recorded owner",
			meta: SessionMeta{ID: delegate, IsSubagent: true, ScratchTreeRootID: root, ScratchDirSessionID: owner},
			want: owner,
		},
		{
			name: "an unparsable owner id is ignored",
			meta: SessionMeta{ID: delegate, IsSubagent: true, ScratchDirSessionID: "not a session id"},
			want: delegate,
		},
		{
			name: "a blank owner on a delegate",
			meta: SessionMeta{ID: delegate, IsSubagent: true},
			want: delegate,
		},
	}
	for _, tc := range cases {
		if got := tc.meta.RestoredScratchDirSession(); got != tc.want {
			t.Errorf("%s: RestoredScratchDirSession() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
