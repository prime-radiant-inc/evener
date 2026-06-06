package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveTranscript turns a model-supplied selector into a concrete file path
// and its opaque ref.
//
// selector:
//   - "" or "current"  → the current session (currentStateDir/sessions/currentSessionID.transcript.jsonl)
//   - "local:<id>"     → the session in the current bucket
//   - "proj:<hash>:<id>" → the session in the named sibling bucket
//   - bare session ID  → search current bucket first, then sibling buckets;
//     ambiguous (found in >1 bucket) → error with candidate refs
func resolveTranscript(selector, currentStateDir, currentSessionID string) (path, ref string, err error) {
	// Empty or "current" → current session.
	// Intentionally no os.Stat: the current session's transcript file may not
	// yet exist (writing is in-progress). Callers must handle a missing file
	// gracefully. (spec §"Lookup and Storage", current-session freshness)
	if selector == "" || selector == "current" {
		p := transcriptPath(currentStateDir, currentSessionID)
		return p, encodeRef("", currentSessionID), nil
	}

	// Try explicit ref (local: or proj:).
	if strings.HasPrefix(selector, "local:") || strings.HasPrefix(selector, "proj:") {
		bucketHash, sessionID, decErr := decodeRef(selector)
		if decErr != nil {
			return "", "", decErr
		}
		var bucketDir string
		if bucketHash == "" {
			// local: — use current bucket
			bucketDir = currentStateDir
		} else {
			// proj: — resolve sibling bucket
			sh := stateHomeFor(currentStateDir)
			if sh == "" {
				return "", "", fmt.Errorf("transcript ref %q: no project root (flat state dir)", selector)
			}
			bucketDir = filepath.Join(sh, "serf", "projects", bucketHash)
		}
		p := transcriptPath(bucketDir, sessionID)
		if _, statErr := os.Stat(p); statErr != nil {
			return "", "", fmt.Errorf("transcript ref %q: transcript not found: %w", selector, statErr)
		}
		return p, encodeRef(bucketHash, sessionID), nil
	}

	// Reject anything with path separators (traversal guard) that wasn't a valid ref.
	if strings.ContainsAny(selector, "/\\") {
		return "", "", fmt.Errorf("invalid session selector %q: contains path separators", selector)
	}

	// Bare session ID — validate as an ID token.
	if err := validIDToken(selector); err != nil {
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
		// Build candidate ref list for the error message.
		var candidates []string
		if currentFound {
			candidates = append(candidates, encodeRef("", selector))
		}
		for _, bucket := range otherMatches {
			candidates = append(candidates, encodeRef(filepath.Base(bucket), selector))
		}
		return "", "", fmt.Errorf("session %q is ambiguous; candidate refs: %s",
			selector, strings.Join(candidates, ", "))
	case currentFound:
		return currentPath, encodeRef("", selector), nil
	default:
		// Exactly one match in a sibling bucket.
		bucket := otherMatches[0]
		bucketHash := filepath.Base(bucket)
		p := transcriptPath(bucket, selector)
		return p, encodeRef(bucketHash, selector), nil
	}
}

// enumerateBuckets returns the state-root dirs under <stateHome>/serf/projects/*.
// It returns bucket roots, not their sessions subdirectories.
func enumerateBuckets(stateHome string) ([]string, error) {
	pattern := filepath.Join(stateHome, "serf", "projects", "*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob project buckets: %w", err)
	}
	// Filter to directories only.
	dirs := make([]string, 0, len(matches))
	for _, m := range matches {
		info, statErr := os.Stat(m)
		if statErr != nil || !info.IsDir() {
			continue
		}
		dirs = append(dirs, m)
	}
	return dirs, nil
}

// stateHomeFor returns the stateHome (the <base> parent two levels above the
// serf/projects/<hash> path) for a bucket state dir, or "" if the state dir is
// not under serf/projects/<hash>.
func stateHomeFor(stateDir string) string {
	// Expect the layout: <stateHome>/serf/projects/<hash>
	// filepath.Dir(stateDir) == <stateHome>/serf/projects
	// filepath.Dir(that)     == <stateHome>/serf
	// filepath.Dir(that)     == <stateHome>
	projects := filepath.Dir(stateDir)
	if filepath.Base(projects) != "projects" {
		return ""
	}
	serf := filepath.Dir(projects)
	if filepath.Base(serf) != "serf" {
		return ""
	}
	return filepath.Dir(serf)
}

// transcriptPath builds the path to a transcript JSONL file.
func transcriptPath(bucketDir, sessionID string) string {
	return filepath.Join(bucketDir, sessionsSubdir, sessionID+".transcript.jsonl")
}
