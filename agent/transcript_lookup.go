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
//     ambiguous (found in >1 bucket) → error with candidate refs
func resolveTranscript(selector, currentStateDir, currentSessionID string) (path, ref string, err error) {
	// Empty or "current" → current session.
	// Intentionally no os.Stat: the current session's transcript file may not
	// yet exist (writing is in-progress). Callers must handle a missing file
	// gracefully. (spec §"Lookup and Storage", current-session freshness)
	if selector == "" || selector == "current" {
		if err := identifier.ValidateSessionID(currentSessionID); err != nil {
			return "", "", fmt.Errorf("invalid current session ID: %w", err)
		}
		p := transcriptPath(currentStateDir, currentSessionID)
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
			// local: — use current bucket
			bucketDir = currentStateDir
		} else {
			// proj: — resolve sibling bucket
			sh := stateHomeFor(currentStateDir)
			if sh == "" {
				return "", "", fmt.Errorf("transcript ref %q: no project root (flat state dir)", selector)
			}
			bucketDir = filepath.Join(sh, "evener", "projects", projectID)
		}
		p := transcriptPath(bucketDir, sessionID)
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

	// Search current bucket first.
	currentPath := transcriptPath(currentStateDir, selector)
	currentFound := false
	if _, statErr := os.Stat(currentPath); statErr == nil {
		currentFound = true
	}

	// Search sibling buckets when a stateHome is available.
	sh := stateHomeFor(currentStateDir)
	var otherMatches []string // bucket dirs (not current) where the session exists
	if sh != "" {
		buckets, globErr := enumerateBuckets(sh)
		if globErr != nil {
			return "", "", fmt.Errorf("enumerating project buckets: %w", globErr)
		}
		currentAbs, _ := filepath.Abs(currentStateDir)
		for _, bucket := range buckets {
			bucketAbs, _ := filepath.Abs(bucket)
			if bucketAbs == currentAbs {
				continue // already checked above
			}
			p := transcriptPath(bucket, selector)
			if _, statErr := os.Stat(p); statErr == nil {
				otherMatches = append(otherMatches, bucket)
			}
		}
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
		return "", "", fmt.Errorf("session %q is ambiguous; found in: %s",
			selector, strings.Join(candidates, ", "))
	case currentFound:
		return currentPath, encodeRef("", selector), nil
	default:
		// Exactly one match in a sibling bucket.
		bucket := otherMatches[0]
		projectID := filepath.Base(bucket)
		p := transcriptPath(bucket, selector)
		return p, refFor(projectID, selector), nil
	}
}

// enumerateBuckets returns the state-root dirs under <stateHome>/evener/projects/*.
// It returns bucket roots, not their sessions subdirectories.
//
// Every directory under projects/ is a bucket: bucket identity is the actual
// directory name, and filtering on identifier.ValidateProjectID would hide a
// legacy- or foreign-named bucket that holds real sessions — the agent-side
// read paths must see what is on disk, mirroring the doctor's globBuckets
// (PR #2163). Refs for grammar-incompatible bucket names are suppressed at
// emission time by refFor, not here.
func enumerateBuckets(stateHome string) ([]string, error) {
	pattern := filepath.Join(stateHome, "evener", "projects", "*")
	matches, err := transcriptBucketGlob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob project buckets: %w", err)
	}
	// Filter to directories only. Use Lstat (not Stat) so symlinks are
	// detected and skipped — a foreign-named symlink under projects/ could
	// point outside the state root and expose transcripts from elsewhere.
	// Mirrors locateLocalJob's entry.Type()&os.ModeSymlink guard.
	dirs := make([]string, 0, len(matches))
	for _, m := range matches {
		info, statErr := os.Lstat(m)
		if statErr != nil || !info.IsDir() {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		dirs = append(dirs, m)
	}
	return dirs, nil
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

// transcriptPath builds the path to a transcript JSONL file.
func transcriptPath(bucketDir, sessionID string) string {
	return filepath.Join(bucketDir, sessionsSubdir, sessionID+".transcript.jsonl")
}

// parentBucketAndID resolves a ref (or bare ID) to the bucket directory and
// session ID of the parent session, without requiring the parent's transcript
// to exist. This is used by execFindChildren: the parent's bucket and ID are
// resolved from the ref when a proj: ref is given, or by statting candidate
// buckets for a bare ID (mirroring resolveTranscript's read path). When the
// parent transcript is not found in any bucket (never-flushed live session),
// parentBucketAndID falls back to the current bucket so children can still be
// found.
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
		return filepath.Join(sh, "evener", "projects", projectID), id, scopeAllProjects, nil
	}
	if err := identifier.ValidateSessionID(selector); err != nil {
		return "", "", "", fmt.Errorf("invalid session selector: %w", err)
	}
	// Bare session ID: resolve cross-bucket by statting candidate buckets,
	// mirroring resolveTranscript's read path. The current bucket is checked
	// first, then sibling buckets. When the parent transcript is not found in
	// any bucket (never-flushed live session), fall back to the current
	// bucket so children_of:"<bare-id>" still works.
	sh := stateHomeFor(currentStateDir)
	if sh == "" {
		return currentStateDir, selector, scopeCurrentProject, nil
	}
	buckets, globErr := enumerateBuckets(sh)
	if globErr != nil {
		return "", "", "", fmt.Errorf("enumerating project buckets: %w", globErr)
	}
	currentAbs, _ := filepath.Abs(currentStateDir)
	var otherMatches []string
	for _, bucket := range buckets {
		bucketAbs, _ := filepath.Abs(bucket)
		if bucketAbs == currentAbs {
			continue
		}
		if _, statErr := os.Stat(transcriptPath(bucket, selector)); statErr == nil {
			otherMatches = append(otherMatches, bucket)
		}
	}
	// Check current bucket too.
	currentFound := false
	if _, statErr := os.Stat(transcriptPath(currentStateDir, selector)); statErr == nil {
		currentFound = true
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
		return "", "", "", fmt.Errorf("session %q is ambiguous; found in: %s",
			selector, strings.Join(candidates, ", "))
	case currentFound:
		return currentStateDir, selector, scopeCurrentProject, nil
	default:
		// Exactly one match in a sibling bucket.
		return otherMatches[0], selector, scopeAllProjects, nil
	}
}
