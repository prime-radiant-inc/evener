package repoaudit

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
)

const testSleepBaseline = "internal/devtool/repoaudit/test_sleep_baseline.txt"

// testSleepsStayWithinBaseline is the ratchet behind the testing doc's "never
// a fixed sleep": the time.Sleep calls in tracked Go tests may not outnumber
// the checked-in baseline. A lower count passes with a note to lower the
// baseline, so a conversion locks in its gain.
//
// A file that imports testing/synctest is left out whole: a sleep inside a
// bubble advances the fake clock and costs nothing. A sleep outside the bubble
// in such a file goes uncounted too, which is the price of a scan that reads
// imports rather than following calls. Files under testdata are fixtures, not
// tests the gate runs.
func testSleepsStayWithinBaseline(a *audit) {
	out, err := exec.CommandContext(context.Background(), "git", "ls-files", "-z", "--", "*_test.go").Output()
	if err != nil {
		a.Fatalf("git ls-files: %v", err)
	}
	count := 0
	for _, file := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if file == "" || slices.Contains(strings.Split(path.Dir(file), "/"), "testdata") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			a.Fatalf("read %s: %v", file, err)
		}
		n, err := countSleeps(file, src)
		if err != nil {
			a.Fatalf("%v", err)
		}
		count += n
	}
	body, err := os.ReadFile(testSleepBaseline)
	if err != nil {
		a.Fatalf("read baseline: %v", err)
	}
	baseline, err := readSleepBaseline(body)
	if err != nil {
		a.Fatalf("%s: %v", testSleepBaseline, err)
	}
	judgeSleeps(a, count, baseline)
}

// judgeSleeps holds count to baseline: above it is a finding, below it a note.
func judgeSleeps(a *audit, count, baseline int) {
	switch {
	case count > baseline:
		a.Errorf("Go tests call time.Sleep %d time(s), over the baseline of %d in %s. "+
			"Await the completion, run the test under testing/synctest, or poll with internal/testwait "+
			"(docs/developing-evener/testing.md, \"Waiting in Go tests\")",
			count, baseline, testSleepBaseline)
	case count < baseline:
		a.Notef("Go tests call time.Sleep %d time(s), under the baseline of %d; set %s to %d",
			count, baseline, testSleepBaseline, count)
	}
}

// countSleeps counts the calls to time.Sleep in one Go file, or none when the
// file imports testing/synctest.
func countSleeps(name string, src []byte) (int, error) {
	file, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	timeName := ""
	for _, imp := range file.Imports {
		switch importPath, _ := strconv.Unquote(imp.Path.Value); importPath {
		case "testing/synctest":
			return 0, nil
		case "time":
			timeName = "time"
			if imp.Name != nil {
				timeName = imp.Name.Name
			}
		}
	}
	if timeName == "" {
		return 0, nil
	}
	count := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Sleep" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == timeName {
			count++
		}
		return true
	})
	return count, nil
}

// readSleepBaseline parses the baseline file: comment lines starting with #,
// blank lines, and one non-negative count.
func readSleepBaseline(body []byte) (int, error) {
	baseline := -1
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if baseline >= 0 {
			return 0, fmt.Errorf("more than one count")
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("count %q is not a non-negative integer", line)
		}
		baseline = n
	}
	if baseline < 0 {
		return 0, fmt.Errorf("no count")
	}
	return baseline, nil
}
