package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/identifier"
)

var transcriptBucketGlob = filepath.Glob

// resolveTranscript turns a model-supplied selector into a concrete file path
// and its opaque ref.
//
// selector:
//   - "" or "current"  → the current session (currentStateDir/sessions/currentSessionID.transcript.jsonl)
//   - "local:<id>"     → the session in the current bucket
//   - "proj:<project-id>:<id>" → the session in the named sibling bucket
//   - bare session ID  → search current bucket first, then sibling buckets;
//     ambiguous (found in >1 bucket) → error listing the buckets it was found in
func resolveTranscript(selector, currentStateDir, currentSessionID string) (path, ref string, err error) {
	// Empty or "current" → current session.
	// Intentionally no os.Stat: the current session's transcript file may not
	// yet exist (writing is in-progress). Callers must handle a missing file
	// gracefully. (spec §"Lookup and Storage", current-session freshness)
	// symlinkErrorDeep returns nil for missing paths, so the freshness
	// guarantee is preserved — the guard only fires on an actual symlink.
	if selector == "" || selector == "current" {
		if err := identifier.ValidateSessionID(currentSessionID); err != nil {
			return "", "", fmt.Errorf("invalid current session ID: %w", err)
		}
		if err := validateLayoutPrefix(currentStateDir); err != nil {
			return "", "", fmt.Errorf("session %q: %w", selector, err)
		}
		p := transcriptPath(currentStateDir, currentSessionID)
		if err := symlinkErrorDeep(p, currentStateDir); err != nil {
			return "", "", fmt.Errorf("session %q: %w", selector, err)
		}
		return p, encodeRef("", currentSessionID), nil
	}

	// Try explicit ref (local: or proj:).
	if strings.HasPrefix(selector, "local:") || strings.HasPrefix(selector, "proj:") {
		projectID, sessionID, decErr := decodeRef(selector)
		if decErr != nil {
			return "", "", decErr
		}
		if err := identifier.ValidateSessionID(sessionID); err != nil {
			return "", "", fmt.Errorf("transcript ref %q: %w", selector, err)
		}
		if projectID != "" {
			if err := identifier.ValidateProjectID(projectID); err != nil {
				return "", "", fmt.Errorf("transcript ref %q: %w", selector, err)
			}
		}
		var bucketDir string
		if projectID == "" {
			// local: — use current bucket. Validate the layout prefix: the
			// bucket dir is used directly, and symlinkErrorDeep below is rooted
			// at the bucket dir, so ancestors above it (evener/,
			// evener/projects/) are not checked.
			if err := validateLayoutPrefix(currentStateDir); err != nil {
				return "", "", fmt.Errorf("transcript ref %q: %w", selector, err)
			}
			bucketDir = currentStateDir
		} else {
			// proj: — resolve sibling bucket. Lstat the joined path and reject
			// symlinks before any Stat/content access: a symlink with a
			// grammar-valid name bypasses enumerateBuckets' symlink skip
			// (enumerateBuckets is not consulted here) and could point outside
			// the state root.
			sh := stateHomeFor(currentStateDir)
			if sh == "" {
				return "", "", fmt.Errorf("transcript ref %q: no project root (flat state dir)", selector)
			}
			bucketDir = filepath.Join(sh, "evener", "projects", projectID)
			if err := symlinkErrorDeep(bucketDir, sh); err != nil {
				return "", "", fmt.Errorf("transcript ref %q: %w", selector, err)
			}
		}
		p := transcriptPath(bucketDir, sessionID)
		// Reject symlinked transcript files (and any symlinked path component,
		// e.g. sessions/) on the read path — a symlink could point outside
		// the state root.
		if err := symlinkErrorDeep(p, bucketDir); err != nil {
			return "", "", fmt.Errorf("transcript ref %q: %w", selector, err)
		}
		if _, statErr := os.Stat(p); statErr != nil {
			return "", "", fmt.Errorf("transcript ref %q: transcript not found: %w", selector, statErr)
		}
		return p, encodeRef(projectID, sessionID), nil
	}

	// Reject anything with path separators (traversal guard) that wasn't a valid ref.
	if strings.ContainsAny(selector, "/\\") {
		return "", "", fmt.Errorf("invalid session selector %q: contains path separators", selector)
	}

	// Bare session ID — validate as an ID token.
	if err := identifier.ValidateSessionID(selector); err != nil {
		return "", "", fmt.Errorf("invalid session selector: %w", err)
	}

	// Search current bucket and sibling buckets via the shared helper.
	sh := stateHomeFor(currentStateDir)
	currentFound, otherMatches, err := findBareIDBuckets(selector, currentStateDir, sh)
	if err != nil {
		return "", "", err
	}

	totalMatches := len(otherMatches)
	if currentFound {
		totalMatches++
	}

	switch {
	case totalMatches == 0:
		return "", "", fmt.Errorf("unknown session %q", selector)
	case totalMatches > 1:
		// List every match as context so the caller can see where the
		// session lives. When refFor returns a usable ref (valid bucket
		// name), include it — the model can pass it back. When refFor
		// returns "" (grammar-incompatible bucket name), include the
		// bucket directory name as context only — a proj: ref for such
		// a name is rejected by the explicit-ref branch, so it is not a
		// usable selector. The bare id alone is ambiguous across these
		// buckets and cannot be resolved to one without changing which
		// bucket is current.
		candidates := ambiguityCandidates(selector, currentFound, otherMatches)
		return "", "", fmt.Errorf("session %q is ambiguous; found in: %s",
			selector, strings.Join(candidates, ", "))
	case currentFound:
		if err := validateLayoutPrefix(currentStateDir); err != nil {
			return "", "", fmt.Errorf("session %q: %w", selector, err)
		}
		currentPath := transcriptPath(currentStateDir, selector)
		if err := symlinkErrorDeep(currentPath, currentStateDir); err != nil {
			return "", "", fmt.Errorf("session %q: %w", selector, err)
		}
		return currentPath, encodeRef("", selector), nil
	default:
		// Exactly one match in a sibling bucket.
		bucket := otherMatches[0]
		projectID := filepath.Base(bucket)
		p := transcriptPath(bucket, selector)
		if err := symlinkErrorDeep(p, sh); err != nil {
			return "", "", fmt.Errorf("session %q: %w", selector, err)
		}
		return p, refFor(projectID, selector), nil
	}
}

// symlinkErrorDeep returns an error if path or any of its existing path
// components between root (exclusive) and path (inclusive) is a symlink, nil
// otherwise. A non-existent path returns nil — the caller's own existence
// check (os.Stat) handles missing files. Symlinked buckets, sessions/
// directories, and transcript files are rejected on the agent-side read
// paths: a symlink can point outside the state root and expose transcripts
// from elsewhere. enumerateBuckets already skips symlinked bucket dirs;
// this guards the explicit proj: branch (which resolves the bucket dir
// directly, bypassing enumeration), the current-session fast-path, and the
// transcript files (including symlinked intermediate sessions/ dirs).
//
// The walk is bounded by root: components at or above root are NOT checked.
// root is the trusted ancestor — the state home (for sibling-bucket paths)
// or the current bucket dir (for current-bucket paths). Checking ancestors
// above the state root is a functional regression on hosts where the state
// root lives under a symlinked path (macOS /var → /private/var, symlinked
// $HOME or $XDG_STATE_HOME): every read would fail even though nothing
// under the state root is a symlink. The threat model covers symlinks
// *within* the state root, not ancestors of a runtime-configured path.
//
// os.Lstat on the final path reports whether that path itself is a symlink,
// but does not detect a symlinked intermediate directory: Lstat follows every
// path element except the last. A component walk that Lstats each prefix
// catches symlinked sessions/ and other intermediate dirs.
func symlinkErrorDeep(path, root string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	// Walk from the longest prefix downward, Lstat each existing component
	// between root (exclusive) and the final path (exclusive). This catches
	// symlinked parent dirs (e.g. sessions/) before the final file.
	dir := filepath.Dir(path)
	for dir != root && dir != "" && dir != string(filepath.Separator) && dir != "." {
		info, _ := os.Lstat(dir)
		if info != nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q traverses a symlink (%q): symlinks are not allowed on the transcript read path", path, dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Lstat the final path itself.
	info, _ := os.Lstat(path)
	if info != nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %q is a symlink: symlinks are not allowed on the transcript read path", path)
	}
	return nil
}

// enumerateBuckets returns the state-root dirs under <stateHome>/evener/projects/*.
// It returns bucket roots, not their sessions subdirectories.
//
// Every directory under projects/ is a bucket: bucket identity is the actual
// directory name, and filtering on identifier.ValidateProjectID would hide a
// legacy- or foreign-named bucket that holds real sessions — the agent-side
// read paths must see what is on disk, like the doctor's globBuckets
// (PR #2163). Refs for grammar-incompatible bucket names are suppressed at
// emission time by refFor, not here.
//
// Symlink policy: enumerateBuckets skips symlinked bucket dirs, a deliberate
// agent-side divergence from the doctor's globBuckets, which follows symlinks
// (its isDir helper uses os.Stat). The doctor is an operator forensic tool
// that must see everything on disk; the agent's model-facing read paths hold
// the higher bar — a symlink under projects/ could point outside the state
// root and expose transcripts from elsewhere, so the agent never follows
// them. The doctor's symlink policy is a separate concern.
func enumerateBuckets(stateHome string) ([]string, error) {
	// Validate the glob pattern for well-formedness BEFORE the prefix
	// not-exists shortcut below. A stateHome containing unmatched glob
	// metacharacters (e.g. an unterminated "[" character class) makes
	// filepath.Glob return ErrBadPattern. Without this check the prefix
	// Lstat would treat the malformed root as a plain not-exists and return
	// (nil, nil), silently accepting a root that previously surfaced the
	// Glob error. filepath.Match uses the same pattern syntax as Glob, so
	// it detects the same ErrBadPattern without touching the filesystem
	// (it cannot follow a symlinked prefix the way Glob would).
	pattern := filepath.Join(stateHome, "evener", "projects", "*")
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, fmt.Errorf("glob project buckets: %w", err)
	}
	// Validate the fixed layout prefix BEFORE globbing: filepath.Glob
	// follows symlinked path components, so a symlinked evener/ or
	// evener/projects/ ancestor (a plausible state-dir relocation) would
	// let glob return buckets whose refs read_transcript then REJECTS with
	// "traverses a symlink". Lstat both prefix dirs and refuse to enumerate
	// when either is a symlink — find must not return refs read rejects.
	// Checking before Glob also avoids calling Glob through a symlinked
	// prefix at all.
	for _, prefix := range []string{
		filepath.Join(stateHome, "evener"),
		filepath.Join(stateHome, "evener", "projects"),
	} {
		info, _ := os.Lstat(prefix)
		if info == nil {
			return nil, nil // prefix does not exist → no buckets
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, nil // symlinked prefix → refuse to enumerate
		}
	}
	matches, err := transcriptBucketGlob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob project buckets: %w", err)
	}
	// Filter to directories only. The symlink check MUST come first and
	// MUST use Lstat (not Stat): Stat follows the symlink, clears
	// ModeSymlink, and would let the symlink through. With Lstat, a symlink
	// reports ModeSymlink and is skipped here; a regular file reports neither
	// ModeSymlink nor IsDir and is skipped by the IsDir check. Mirrors
	// locateLocalJob's entry.Type()&os.ModeSymlink guard.
	dirs := make([]string, 0, len(matches))
	for _, m := range matches {
		info, statErr := os.Lstat(m)
		if statErr != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() {
			continue
		}
		dirs = append(dirs, m)
	}
	return dirs, nil
}

// findBareIDBuckets stats the current bucket and all sibling buckets (via
// enumerateBuckets) for a bare session ID, returning which buckets contain the
// transcript. Existence is checked via symlinkErrorDeep + os.Lstat (not
// os.Stat) so symlinked transcript files AND symlinked sessions/ dirs are
// skipped entirely — they never enter the match set. This prevents a
// symlinked sessions/ dir or a symlinked file in one bucket from producing a
// spurious "ambiguous" error when a real file exists in another bucket. When
// stateHome is empty (flat layout), no sibling search is performed and
// otherMatches is nil. Callers apply their own policy for the zero-match case
// (resolveTranscript returns "unknown session"; parentBucketAndID falls back
// to the current bucket).
func findBareIDBuckets(selector, currentStateDir, stateHome string) (currentFound bool, otherMatches []string, err error) {
	if existsNonSymlink(transcriptPath(currentStateDir, selector), currentStateDir) {
		currentFound = true
	}
	if stateHome == "" {
		return currentFound, nil, nil
	}
	buckets, globErr := enumerateBuckets(stateHome)
	if globErr != nil {
		return false, nil, fmt.Errorf("enumerating project buckets: %w", globErr)
	}
	currentAbs, _ := filepath.Abs(currentStateDir)
	for _, bucket := range buckets {
		bucketAbs, _ := filepath.Abs(bucket)
		if bucketAbs == currentAbs {
			continue // already checked above
		}
		if existsNonSymlink(transcriptPath(bucket, selector), bucket) {
			otherMatches = append(otherMatches, bucket)
		}
	}
	return currentFound, otherMatches, nil
}

// existsNonSymlink returns true if the transcript file exists, is not a
// symlink, and no intermediate component (sessions/) is a symlink. Uses
// symlinkErrorDeep with the bucket dir as root so the sessions/ dir and the
// file itself are both checked. A symlinked sessions/ dir containing a real
// file must not count as a match — it points outside the state root.
func existsNonSymlink(path, bucketDir string) bool {
	if err := symlinkErrorDeep(path, bucketDir); err != nil {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink == 0
}

// ambiguityCandidates builds the context list for a bare-ID ambiguity error.
// When refFor returns a usable ref (valid bucket name), include it — the
// model can pass it back. When refFor returns "" (grammar-incompatible bucket
// name), include the bucket directory name as context only — a proj: ref for
// such a name is rejected by the explicit-ref branch, so it is not a usable
// selector. Shared by resolveTranscript and parentBucketAndID so the two
// cannot drift apart.
func ambiguityCandidates(selector string, currentFound bool, otherMatches []string) []string {
	var candidates []string
	if currentFound {
		candidates = append(candidates, encodeRef("", selector))
	}
	for _, bucket := range otherMatches {
		if r := refFor(filepath.Base(bucket), selector); r != "" {
			candidates = append(candidates, r)
		} else {
			candidates = append(candidates, filepath.Base(bucket))
		}
	}
	return candidates
}

// refFor builds a transcript ref only when the bucket name is consumable by
// the shared agent ref grammar (identifier.ValidateProjectID). A bucket whose
// directory name the grammar rejects gets no ref — mirroring the doctor's
// refFor from #2163. Such sessions stay locatable by bare id (the enumeration
// no longer filters them out) and addressable by explicit local: refs, but no
// proj: ref is emitted for a name read_transcript / find_session_transcripts
// would reject.
func refFor(projectID, sessionID string) string {
	if projectID == "" {
		return encodeRef("", sessionID)
	}
	if identifier.ValidateProjectID(projectID) != nil {
		return ""
	}
	return encodeRef(projectID, sessionID)
}

// stateHomeFor returns the stateHome for a bucket state dir via the shared
// layout helper (userdirs.StateHomeForBucketDir — also used by the doctor's
// cross-bucket sweep, so the two cannot drift apart).
func stateHomeFor(stateDir string) string {
	return userdirs.StateHomeForBucketDir(stateDir)
}

// validateLayoutPrefix Lstats the stateHome/evener and stateHome/evener/projects
// ancestors of an in-layout bucket dir, returning an error if either is a
// symlink. This mirrors enumerateBuckets' prefix guard: symlinkErrorDeep rooted
// at the bucket dir does not check ancestors above the bucket, so a symlinked
// evener/ (a plausible state-dir relocation where ~/.local/state/evener itself
// is a symlink within the state root) would let current-bucket reads resolve
// through it while sibling reads and explicit proj: refs are rejected — an
// inconsistency. For flat layouts (no state home), validateLayoutPrefix is a
// no-op: there are no layout ancestors to check.
func validateLayoutPrefix(stateDir string) error {
	sh := stateHomeFor(stateDir)
	if sh == "" {
		return nil // flat layout — no layout prefix to validate
	}
	for _, prefix := range []string{
		filepath.Join(sh, "evener"),
		filepath.Join(sh, "evener", "projects"),
	} {
		info, _ := os.Lstat(prefix)
		if info == nil {
			return nil // prefix does not exist — not our concern
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("state dir %q: layout prefix %q is a symlink: symlinks are not allowed on the transcript read path", stateDir, prefix)
		}
	}
	return nil
}

// transcriptPath builds the path to a transcript JSONL file.
func transcriptPath(bucketDir, sessionID string) string {
	return filepath.Join(bucketDir, sessionsSubdir, sessionID+".transcript.jsonl")
}

// parentBucketAndID resolves a ref (or bare ID) to the bucket directory and
// session ID of the parent session, without requiring the parent's transcript
// to exist. This is used by execFindChildren: the parent's bucket and ID are
// resolved from the ref when a proj: ref is given, or by statting candidate
// buckets for a bare ID (via the shared findBareIDBuckets helper, the same
// search resolveTranscript uses). When the parent transcript is not found in
// any bucket (never-flushed live session), parentBucketAndID falls back to
// the current bucket so children can still be found.
//
// Returns the resolved bucket dir, the parent session ID, the scope that
// applies (current_project or all_projects), and any parse error.
func parentBucketAndID(selector, currentStateDir, currentSessionID string) (bucketDir, parentID, scopeApplied string, err error) {
	if selector == "" || selector == "current" {
		if err := identifier.ValidateSessionID(currentSessionID); err != nil {
			return "", "", "", fmt.Errorf("invalid current session ID: %w", err)
		}
		return currentStateDir, currentSessionID, scopeCurrentProject, nil
	}
	if strings.HasPrefix(selector, "local:") || strings.HasPrefix(selector, "proj:") {
		projectID, id, decErr := decodeRef(selector)
		if decErr != nil {
			return "", "", "", decErr
		}
		if err := identifier.ValidateSessionID(id); err != nil {
			return "", "", "", fmt.Errorf("invalid session selector: %w", err)
		}
		if projectID != "" {
			if err := identifier.ValidateProjectID(projectID); err != nil {
				return "", "", "", fmt.Errorf("invalid project selector: %w", err)
			}
		}
		if projectID == "" {
			return currentStateDir, id, scopeCurrentProject, nil
		}
		sh := stateHomeFor(currentStateDir)
		if sh == "" {
			return "", "", "", fmt.Errorf("transcript ref %q: no project root (flat state dir)", selector)
		}
		bucket := filepath.Join(sh, "evener", "projects", projectID)
		// Reject symlinked buckets on the explicit proj: path, matching
		// resolveTranscript's guard. parentBucketAndID does not stat the
		// transcript, but the bucket dir it returns is used to search for
		// children — a symlinked bucket would expose children outside the
		// state root.
		if err := symlinkErrorDeep(bucket, sh); err != nil {
			return "", "", "", fmt.Errorf("transcript ref %q: %w", selector, err)
		}
		return bucket, id, scopeAllProjects, nil
	}
	if err := identifier.ValidateSessionID(selector); err != nil {
		return "", "", "", fmt.Errorf("invalid session selector: %w", err)
	}
	// Bare session ID: resolve cross-bucket via the shared helper, mirroring
	// resolveTranscript's read path. When the parent transcript is not found
	// in any bucket (never-flushed live session), fall back to the current
	// bucket so children_of:"<bare-id>" still works.
	sh := stateHomeFor(currentStateDir)
	if sh == "" {
		return currentStateDir, selector, scopeCurrentProject, nil
	}
	currentFound, otherMatches, err := findBareIDBuckets(selector, currentStateDir, sh)
	if err != nil {
		return "", "", "", err
	}
	totalMatches := len(otherMatches)
	if currentFound {
		totalMatches++
	}
	switch {
	case totalMatches == 0:
		// Parent transcript not found in any bucket. The parent may be the
		// live session (transcript not yet flushed) or a parent whose
		// transcript was pruned. Fall back to the current bucket so
		// children_of:"" / children_of:"current" still works, and
		// children_of:<bare-id> for a never-flushed parent still searches
		// the current bucket.
		return currentStateDir, selector, scopeCurrentProject, nil
	case totalMatches > 1:
		// Ambiguous: the bare ID exists in multiple buckets. Mirror
		// resolveTranscript's ambiguity error with bucket names as context.
		candidates := ambiguityCandidates(selector, currentFound, otherMatches)
		return "", "", "", fmt.Errorf("session %q is ambiguous; found in: %s",
			selector, strings.Join(candidates, ", "))
	case currentFound:
		return currentStateDir, selector, scopeCurrentProject, nil
	default:
		// Exactly one match in a sibling bucket.
		return otherMatches[0], selector, scopeAllProjects, nil
	}
}
