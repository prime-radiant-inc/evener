package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The package doc comment's Usage block is the canonical listing of every
// serf-doctor subcommand and flag: it is what `go doc` renders, what
// cmd/serf-doctor/README.md and the bundled doctoring-serf skill were written
// from, and the first thing anyone reads before reaching for a flag. Nothing
// tied it to the flags the subcommands actually register, so it drifted:
// kata 6tr1 found `locate [--all-buckets]` advertised there (and copied onward
// into the README and the skill) for a flag that has never existed — locate
// registers only --state-dir and --json, so the documented invocation dies at
// parse time with "flag provided but not defined: -all-buckets" and exit 2.
//
// This audit derives its expectations from the doc comment itself rather than
// from a hand-maintained list, so a newly documented flag is checked the moment
// it is written down and no allowlist can go stale.

// usageCommentLineRE matches one invocation line of the doc comment's Usage
// block — the tab-indented `//\tserf-doctor <sub> …` form gofmt renders as a
// code block. Prose lines in the same comment (the "Common flags:" sentence)
// are deliberately not matched: they name flags shared by every subcommand
// rather than flags of one, and have no subcommand to test them against.
var usageCommentLineRE = regexp.MustCompile(`^//\t(?:serf-doctor) ([a-z][a-z-]*) +(.*)$`)

// usageCommentFlagRE pulls the `--flag` tokens out of one such line.
var usageCommentFlagRE = regexp.MustCompile(`--([a-z][a-z0-9-]*)`)

// TestUsageCommentAdvertisesOnlyRegisteredFlags runs every flag the doc
// comment advertises through the real dispatcher and fails on the one error
// that means the flag does not exist. A deliberately junk value is passed so
// string, bool and int flags all take the same shape; an invalid *value* is a
// different error and a registered flag, which is all this test asks about.
func TestUsageCommentAdvertisesOnlyRegisteredFlags(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	stateDir := t.TempDir()

	var offenders []string
	var checked int
	for i, line := range strings.Split(string(src), "\n") {
		m := usageCommentLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		sub := m[1]
		for _, f := range usageCommentFlagRE.FindAllStringSubmatch(m[2], -1) {
			name := f[1]
			checked++
			var stderr bytes.Buffer
			run([]string{sub, "local:no-such-session", "--" + name + "=x", "--state-dir", stateDir}, io.Discard, &stderr)
			if strings.Contains(stderr.String(), "flag provided but not defined") {
				offenders = append(offenders, fmt.Sprintf("main.go:%d: `serf-doctor %s --%s` — %s registers no such flag",
					i+1, sub, name, sub))
			}
		}
	}

	if checked == 0 {
		t.Fatal("parsed no flags out of main.go's doc-comment Usage block — the block " +
			"moved or changed shape, so this audit is checking nothing. Fix usageCommentLineRE.")
	}
	if len(offenders) > 0 {
		t.Fatalf("main.go's doc comment advertises %d flag(s) that no subcommand registers, "+
			"so the documented invocation exits 2 at parse time. Either register the flag or "+
			"stop advertising it — and check cmd/serf-doctor/README.md and "+
			"internal/bundled/skills/doctoring-serf/SKILL.md, which are written from this block:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}
