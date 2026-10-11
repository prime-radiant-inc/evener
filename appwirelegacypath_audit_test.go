package evener_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The legacy AppWire browser client lived under the serf-hub assets directory
// and was deleted in 660376f784 ("webui m10: delete legacy assets"). The
// cmd/serf-* directories were only renamed to cmd/evener-* later (5fd3fc2d65),
// so the file never existed under the evener-hub assets directory: `git log
// --all --` for that path is empty while the serf-hub path resolves.
//
// A comment or doc that names the evener-hub path sends a reader tracing the
// mirrored behavior to a path no commit ever created. Both needles are built by
// concatenation so this file does not match its own scan.
var (
	appwireLegacyWrongPath = "cmd/evener-hub/" + "assets/appwire.js"
	appwireLegacyRealPath  = "cmd/serf-hub/" + "assets/appwire.js"
)

// TestAppwireLegacyPathIsTheHistoricalOne keeps every surviving reference to the
// deleted AppWire client pointing at the path the file actually had, so a reader
// can still find it through git history. It fails while the evener-hub path is
// present anywhere in the tracked tree and passes once the references name the
// serf-hub path the file lived at (issue #2819).
func TestAppwireLegacyPathIsTheHistoricalOne(t *testing.T) {
	t.Parallel()
	tracked := appwireLegacyTrackedFiles(t)

	var findings []string
	realHits := 0
	for _, path := range tracked {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if bytes.IndexByte(raw, 0) >= 0 {
			continue // binary; no path references to read
		}
		text := string(raw)
		if !strings.Contains(text, appwireLegacyWrongPath) &&
			!strings.Contains(text, appwireLegacyRealPath) {
			continue
		}
		for i, line := range strings.Split(text, "\n") {
			if strings.Contains(line, appwireLegacyWrongPath) {
				findings = append(findings, path+":"+strconv.Itoa(i+1)+
					": "+strings.TrimSpace(line))
			}
			if strings.Contains(line, appwireLegacyRealPath) {
				realHits++
			}
		}
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("the legacy AppWire client never lived at %q — it lived at %q "+
			"and was deleted in 660376f784; the cmd/serf-* -> cmd/evener-* rename "+
			"(5fd3fc2d65) came later, so `git log --all -- %s` is empty. A reader "+
			"tracing the mirrored behavior follows a path no commit created. "+
			"Correct each reference to the serf-hub path (#2819):\n%s",
			appwireLegacyWrongPath, appwireLegacyRealPath, appwireLegacyWrongPath,
			strings.Join(findings, "\n"))
	}
	// A scan is green either because the tree is clean or because its needle
	// stopped matching; the corrected path must still appear somewhere, so
	// deleting every mention cannot pass as a fix.
	if realHits == 0 {
		t.Fatalf("no tracked file names %q — the references were deleted rather "+
			"than corrected, or this needle broke; #2819 asks to correct them to "+
			"the path the file actually had", appwireLegacyRealPath)
	}
}

// appwireLegacyTrackedFiles returns every git-tracked file in the worktree, so
// the audit reads the repository as committed and not scratch, build output, or
// a gitignored dependency directory.
func appwireLegacyTrackedFiles(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for path := range strings.SplitSeq(string(out), "\x00") {
		if path == "" {
			continue
		}
		files = append(files, path)
	}
	if len(files) == 0 {
		t.Fatal(errors.New("git ls-files listed no tracked files — the audit is reading nothing"))
	}
	return files
}
