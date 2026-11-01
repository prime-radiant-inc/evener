package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	agentsandbox "primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// The scratch root is the third boundary the doc routes serve beside the
// working directory and the retained artifacts directory. A session's tools
// write screenshots and other session-local output into its named scratch
// directory and name those files by absolute path in messages and tool
// results, so the routes have to serve them — never anything beyond the
// session's own directory, which the resolver below finds from the session's
// recorded meta the same way the archiving removal path finds its tree.

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
	meta, err := schema.LoadSessionMeta(stateDir, session)
	if err != nil {
		return ""
	}
	return scratchDirFromMeta(meta)
}

// scratchDirFromMeta answers the scratch directory meta's session names when
// one exists on disk. The tree root comes from the meta itself — the session's
// own id, or the root that spawned a delegate — and the bases are the ones the
// allocator chooses between and the removal path reclaims from: the temp dir
// the session's daemon recorded, this process's own, then the user cache dir.
// The first existing real directory wins; anything else (no scratch yet, a
// cleaned tree, a symlink or file planted at the path) serves nothing.
func scratchDirFromMeta(meta schema.SessionMeta) string {
	if schema.ValidateSessionID(meta.ID) != nil {
		return ""
	}
	treeRoot := meta.RestoredScratchTreeRoot()
	for _, base := range agentsandbox.SessionScratchTreeBases(meta.ScratchTempDir) {
		dir := agentsandbox.SessionScratchDirPath(base, treeRoot, meta.ID)
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
// scratch directory for the doc routes, answering the containment question
// artifactsDocTarget answers for the artifacts root: the root, the resolved
// path, whether a refused path was still contained by the root (a miss the
// caller reports as not-found rather than forbidden), and whether the path
// names a readable file there. A session fenced for deletion serves nothing
// from its scratch, under both identities a stable ref can name, exactly as
// the artifacts root is fenced.
func scratchDocTarget(ctx context.Context, cfg hubcore.WebConfig, session, rel string) (root, abs string, contained, ok bool) {
	if !filepath.IsAbs(strings.TrimSpace(rel)) {
		return "", "", false, false
	}
	_, resolvedSession, resolved := docSessionScope(ctx, cfg, session)
	fenced := []string{canonicalRouteID(session)}
	if resolved && resolvedSession != "" && resolvedSession != fenced[0] {
		fenced = append(fenced, resolvedSession)
	}
	for _, identity := range fenced {
		if err := deletionFenceError(cfg, appRefFromRouteID(identity), identity, ""); err != nil {
			return "", "", false, false
		}
	}
	root = sessionScratchRoot(ctx, cfg, session)
	if root == "" {
		return "", "", false, false
	}
	abs, err := fspaths.ResolveInRoot(root, rel)
	if err != nil {
		return root, "", !errors.Is(err, fspaths.ErrPathEscapesRoot), false
	}
	return root, abs, true, true
}

// resolveScratchDocTarget is resolveSessionFile's scratch branch for the
// local doc routes: it answers the request itself when it cannot, so every
// local doc route refuses an unknown session, an escape attempt and a missing
// path identically to the cwd and artifacts branches.
func (s *WebServer) resolveScratchDocTarget(ctx context.Context, session, rel string) (string, string, bool) {
	root, abs, _, ok := scratchDocTarget(ctx, s.cfg, session, rel)
	return root, abs, ok
}
