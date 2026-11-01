package hub

import (
	"context"
	"os"

	agentsandbox "primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// The scratch root is the third boundary the doc routes serve beside the
// working directory and the retained artifacts directory, found from the
// session's recorded meta the same way the archiving removal path finds its
// tree; see resolveSessionFile for the shared serving rule.

// sessionScratchRoot is the local session's named scratch directory, or ""
// when this hub serves none for it. The argument may be a session id or the
// stable workspace ref a thread keeps across Clear; a scratch belongs to the
// same session identity the artifacts root does, so it resolves through the
// same scope. A remote session names no local path.
func sessionScratchRoot(ctx context.Context, cfg hubcore.WebConfig, ref string) string {
	stateDir, session, ok := docSessionScope(ctx, cfg, ref)
	if !ok {
		return ""
	}
	// A session the roster is not running has its meta in the past index's
	// cache already; reading that copy keeps a list of finished sessions
	// from re-reading and re-decoding one meta file per thread. A roster-live
	// session keeps the fresh disk read — a resume under a restarted daemon
	// may have rewritten the recorded scratch temp base — and the worst a
	// stale cached copy can do is name a base that holds no directory, which
	// answers no root rather than a wrong one.
	if !rosterOwnsLiveSession(cfg.Roster, session) && cfg.Past != nil {
		if entry, found := cfg.Past.FindIndexed(session); found {
			return scratchDirFromMeta(entry.Meta)
		}
	}
	meta, err := schema.LoadSessionMeta(stateDir, session)
	if err != nil {
		return ""
	}
	return scratchDirFromMeta(meta)
}

// scratchDirFromMeta answers the scratch directory meta's session names when
// one exists on disk. Both identity components come from the meta itself —
// the tree through RestoredScratchTreeRoot, the directory's owning session
// through RestoredScratchDirSession, which resolves the owner a delegate
// that shares its parent's environment records — and the bases are the ones
// the allocator chooses between and the removal path reclaims from: the temp
// dir the session's daemon recorded, this process's own, then the user cache
// dir. The first existing real directory wins; anything else (no scratch yet,
// a cleaned tree, a symlink or file planted at the path) serves nothing.
func scratchDirFromMeta(meta schema.SessionMeta) string {
	if schema.ValidateSessionID(meta.ID) != nil {
		return ""
	}
	treeRoot := meta.RestoredScratchTreeRoot()
	owner := meta.RestoredScratchDirSession()
	for _, base := range agentsandbox.SessionScratchTreeBases(meta.ScratchTempDir) {
		dir := agentsandbox.SessionScratchDirPath(base, treeRoot, owner)
		if dir == "" {
			continue
		}
		if info, err := os.Lstat(dir); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return dir
		}
	}
	return ""
}

// scratchDocTarget resolves an absolute request path against the session's
// scratch directory for the doc routes: retainedDocTarget's shared rule over
// the fence, containment and miss classification, with sessionScratchRoot as
// the root this hub serves.
func scratchDocTarget(ctx context.Context, cfg hubcore.WebConfig, session, rel string) (root, abs string, contained, ok bool) {
	return retainedDocTarget(ctx, cfg, session, rel, sessionScratchRoot)
}
