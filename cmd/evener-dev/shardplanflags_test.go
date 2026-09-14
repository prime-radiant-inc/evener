package dev

import (
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// TestEveryDocumentedFlagIsClassified is what makes "exhaustive" true rather
// than asserted. The tables above claim to cover every flag `go help build`
// and `go help testflag` document; this reads those two pages from the
// toolchain in use and fails naming anything none of the tables place.
//
// So a toolchain that adds a flag -- go1.27.0 has no -godebug, a later one
// may -- fails here rather than dropping it silently into a build that was
// never asked for.
func TestEveryDocumentedFlagIsClassified(t *testing.T) {
	t.Parallel()
	classified := func(name string) bool {
		return buildValueFlags[name] || buildBareFlags[name] ||
			testForwardValueFlags[name] || testForwardBareFlags[name] ||
			testRefusedValueFlags[name] || testRefusedBareFlags[name] ||
			// -short and -v have their own branches in parseFlags, since one
			// decides the survey's mode and the other is the shards' own.
			name == "-short" || name == "-v"
	}
	var unclassified []string
	for _, page := range []string{"build", "testflag"} {
		for _, name := range documentedFlags(t, page) {
			if !classified(name) {
				unclassified = append(unclassified, "go help "+page+": "+name)
			}
		}
	}
	if len(unclassified) > 0 {
		t.Fatalf("the toolchain documents flags no table here places, so they would be refused as unknown "+
			"or -- worse -- read as something else. Put each in the table it belongs to:\n  %s",
			strings.Join(unclassified, "\n  "))
	}
}

// documentedFlags reads the flag names out of one `go help` page. The pages
// list each flag on its own indented line, name first; a name written with an
// inline value in the prose (-buildvcs=true) counts as the flag itself, and
// the test binary's own -test. spellings are the same flags again.
func documentedFlags(t *testing.T, page string) []string {
	t.Helper()
	cmd := exec.Command("go", "help", page)
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOENV=off")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go help %s: %v", page, err)
	}
	seen := map[string]bool{}
	var names []string
	for line := range strings.Lines(string(out)) {
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "    ") {
			continue
		}
		field := strings.Fields(line)
		if len(field) == 0 || !strings.HasPrefix(field[0], "-") {
			continue
		}
		name := goFlag(field[0])
		if i := strings.IndexByte(name, '='); i > 0 {
			name = name[:i]
		}
		// A dash on its own, or a stray dash-led word in prose, is not a flag.
		if len(name) < 2 || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		t.Fatalf("go help %s listed no flags; the parser is broken, not the toolchain", page)
	}
	return names
}

// TestEveryFlagIsInExactlyOneTable closes the other half of the
// classification: a name in two tables is read by whichever branch runs first,
// which is a decision nobody made. -vet was in the build table and the refused
// table at once, and the reorder that put the test side first turned a build
// flag into a refusal.
func TestEveryFlagIsInExactlyOneTable(t *testing.T) {
	t.Parallel()
	tables := []struct {
		name  string
		flags map[string]bool
	}{
		{"buildValueFlags", buildValueFlags},
		{"buildBareFlags", buildBareFlags},
		{"testForwardValueFlags", testForwardValueFlags},
		{"testForwardBareFlags", testForwardBareFlags},
		{"testRefusedValueFlags", testRefusedValueFlags},
		{"testRefusedBareFlags", testRefusedBareFlags},
	}
	where := map[string][]string{}
	for _, table := range tables {
		for name := range table.flags {
			where[name] = append(where[name], table.name)
		}
	}
	for name, tableNames := range where {
		if len(tableNames) > 1 {
			sort.Strings(tableNames)
			t.Errorf("%s is in %s: one of them decides what happens to it, and which one is an accident of the order the branches are written in",
				name, strings.Join(tableNames, " and "))
		}
	}
}
