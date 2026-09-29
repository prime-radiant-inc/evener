package main

import (
	"go/format"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// promptEvalTasksDir holds the prompt rewrite's evaluation tasks.
const promptEvalTasksDir = "../../../prompt-eval/tasks"

// TestPromptEvalTasks checks every evaluation task offline. Each manifest
// decodes with no unknown fields, so a misspelled field cannot silently turn
// a check off. When a task has a reference solution, its checks fail on the
// untouched fixture and pass after the solution. A task with no reference
// asks for no change, so its checks pass untouched. A task whose checks pass
// before the agent acts, or that no solution can pass, would measure nothing.
func TestPromptEvalTasks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("task checks run under bash")
	}
	paths, err := filepath.Glob(filepath.Join(promptEvalTasksDir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("no tasks under %s", promptEvalTasksDir)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			probe := decodeTaskStrict(t, path)
			if !strings.HasPrefix(probe.ID, "prose.") {
				t.Errorf("id %q, want the prose. prefix", probe.ID)
			}
			if strings.TrimSpace(probe.Prompt) == "" {
				t.Fatal("empty prompt")
			}
			if bad := unformattedFixtureFiles(probe.Fixture); len(bad) > 0 {
				t.Errorf("fixture Go files %v do not parse or are not gofmt-formatted, so an agent that formats the tree would change them", bad)
			}
			for _, c := range probe.Expect.Checks {
				if c.Name == "" || c.Run == "" {
					t.Fatalf("check %+v needs a name and a command", c)
				}
			}
			if len(probe.Expect.Checks) == 0 {
				return
			}
			untouched := filepath.Join(t.TempDir(), "work")
			if err := materializeFixture(untouched, probe.Fixture); err != nil {
				t.Fatal(err)
			}
			failing := failingChecks(untouched, probe.Expect.Checks)
			if probe.Reference == "" {
				if len(failing) > 0 {
					t.Fatalf("checks %v fail on the untouched fixture, and the task has no reference solution", failing)
				}
				return
			}
			if len(failing) == 0 {
				t.Fatal("every check passes before the agent acts, so the task measures nothing")
			}
			solved := filepath.Join(t.TempDir(), "work")
			if err := materializeFixture(solved, probe.Fixture); err != nil {
				t.Fatal(err)
			}
			if ok, detail := runCheck(solved, checkSpec{Name: "reference", Run: probe.Reference}, 3*time.Minute); !ok {
				t.Fatalf("reference solution failed: %s", detail)
			}
			if failing := failingChecks(solved, probe.Expect.Checks); len(failing) > 0 {
				t.Fatalf("checks %v fail after the reference solution", failing)
			}
		})
	}
}

func decodeTaskStrict(t *testing.T, path string) probeFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := decodeProbe(data)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return probe
}

// unformattedFixtureFiles names the fixture's Go files that do not parse or
// that gofmt would change. An agent that formats the tree must leave the
// fixture as it was, or a check that diffs against the fixture's first commit,
// such as "export changed", passes or fails for a reason that has nothing to
// do with the prompt.
func unformattedFixtureFiles(fixture fixtureSpec) []string {
	var bad []string
	for _, files := range []map[string]string{fixture.Files, fixture.Untracked} {
		for name, content := range files {
			if !strings.HasSuffix(name, ".go") {
				continue
			}
			if formatted, err := format.Source([]byte(content)); err != nil || string(formatted) != content {
				bad = append(bad, name)
			}
		}
	}
	slices.Sort(bad)
	return bad
}

func TestUnformattedFixtureFilesFindsSpacesAndParseErrors(t *testing.T) {
	t.Parallel()
	got := unformattedFixtureFiles(fixtureSpec{
		Files: map[string]string{
			"spaces.go": "package a\n\nfunc F() {\n    return\n}\n",
			"broken.go": "package b\n\nfunc {\n",
			"fine.go":   "package c\n",
			"notes.txt": "  not Go\n",
		},
		Untracked: map[string]string{"loose.go": "package d\n\nfunc   G() {}\n"},
	})
	if want := []string{"broken.go", "loose.go", "spaces.go"}; !slices.Equal(got, want) {
		t.Errorf("unformattedFixtureFiles = %v, want %v", got, want)
	}
}

// TestShippedProbesDecodeStrictly: the runner refuses unknown fields, so every
// probe manifest in the repository has to decode strictly.
func TestShippedProbesDecodeStrictly(t *testing.T) {
	t.Parallel()
	probes, err := loadProbes(filepath.Join("..", "..", "probes"), "all")
	if err != nil || len(probes) == 0 {
		t.Fatalf("loadProbes = %d probes, %v", len(probes), err)
	}
}

// TestLoadProbesRefusesAMisspelledField: the runner decodes every manifest
// strictly, including the copies a lab runs from, so a misspelled field
// cannot silently turn a check off.
func TestLoadProbesRefusesAMisspelledField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "task.yaml"), "schema: 1\nid: prose.x\nprompt: p\nexpect:\n  allow_tool_error: true\n")
	if _, err := loadProbes(dir, "all"); err == nil || !strings.Contains(err.Error(), "allow_tool_error") {
		t.Fatalf("loadProbes = %v, want an error naming the misspelled field", err)
	}
}

// TestAmbiguousExportChecksSurviveACommittedFix: ambiguous-export.yaml's
// checks must still report correctly when the agent commits its fix, not
// only when the fix is left sitting uncommitted in the work tree. A check
// written against HEAD breaks the moment the agent commits: HEAD then IS the
// fix, so the "export changed" check would wrongly say nothing changed.
func TestAmbiguousExportChecksSurviveACommittedFix(t *testing.T) {
	t.Parallel()
	probe := decodeTaskStrict(t, filepath.Join(promptEvalTasksDir, "ambiguous-export.yaml"))
	work := filepath.Join(t.TempDir(), "work")
	if err := materializeFixture(work, probe.Fixture); err != nil {
		t.Fatal(err)
	}
	if ok, detail := runCheck(work, checkSpec{Name: "reference", Run: probe.Reference}, 3*time.Minute); !ok {
		t.Fatalf("reference solution failed: %s", detail)
	}
	// The agent commits its fix, as an agent's own workflow might.
	fixtureGit(t, work, "add", "-A")
	fixtureGit(t, work, "commit", "-q", "-m", "agent's commit")

	if failing := failingChecks(work, probe.Expect.Checks); len(failing) > 0 {
		t.Fatalf("checks %v fail once the agent's fix is committed", failing)
	}
}

// TestFirstCommitCheckJudgesACommittedChangeCorrectly: a task's "file
// changed" check must still see the change when the agent commits it, not
// only when the change is left uncommitted. A check written against HEAD
// breaks the moment the agent commits: HEAD then IS the change, so
// `git diff HEAD` shows nothing and the check wrongly says the file never
// changed. A check written against the fixture's first commit
// ($(git rev-list --max-parents=0 HEAD)) stays correct either way, since
// that commit never moves. This is the fix applied to ambiguous-export.yaml,
// bugfix-tally.yaml, and delegate-textutil.yaml.
func TestFirstCommitCheckJudgesACommittedChangeCorrectly(t *testing.T) {
	t.Parallel()
	work := filepath.Join(t.TempDir(), "work")
	if err := materializeFixture(work, fixtureSpec{
		Git:   true,
		Files: map[string]string{"export.go": "package export\n\nfunc F() int { return 1 }\n"},
	}); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(work, "export.go"), "package export\n\nfunc F() int { return 2 }\n")
	fixtureGit(t, work, "add", "-A")
	fixtureGit(t, work, "commit", "-q", "-m", "agent's commit")

	firstCommitCheck := checkSpec{Name: "export changed", Run: `! git diff --quiet $(git rev-list --max-parents=0 HEAD) -- export.go`}
	if ok, detail := runCheck(work, firstCommitCheck, checkTimeout); !ok {
		t.Errorf("check against the fixture's first commit failed after the agent committed its change: %s", detail)
	}

	headCheck := checkSpec{Name: "export changed (against HEAD, the bug this replaces)", Run: `! git diff --quiet HEAD -- export.go`}
	if ok, _ := runCheck(work, headCheck, checkTimeout); ok {
		t.Error("check against HEAD passed after the agent committed its change; it should wrongly fail here, which is exactly why HEAD was the wrong comparison")
	}
}

// TestOriginalTestsPassAllowsAddedTestsAndRefusesWeakenedOnes: the tasks
// guard against an agent weakening the tests it was given, not against an
// agent adding coverage. The check runs the fixture's original test files
// against the agent's code, with none of the agent's own test files, so a
// correct fix with added tests passes, and replacing the original tests or
// adding a TestMain that exits early gains nothing.
func TestOriginalTestsPassAllowsAddedTestsAndRefusesWeakenedOnes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		task, testFile string
		// untouchedFails: the fixture's own code fails its original tests,
		// so weakened tests with no fix must fail the check.
		untouchedFails bool
	}{
		{"ambiguous-export.yaml", "export/export_test.go", false},
		{"bugfix-tally.yaml", "tally/sum_test.go", true},
		{"delegate-textutil.yaml", "textutil/textutil_test.go", true},
	} {
		t.Run(tc.task, func(t *testing.T) {
			t.Parallel()
			probe := decodeTaskStrict(t, filepath.Join(promptEvalTasksDir, tc.task))
			var check checkSpec
			for _, c := range probe.Expect.Checks {
				if c.Name == "original tests pass" {
					check = c
				}
			}
			if check.Run == "" {
				t.Fatalf("%s has no %q check", tc.task, "original tests pass")
			}
			original := probe.Fixture.Files[tc.testFile]
			pkgLine, _, _ := strings.Cut(original, "\n")
			dir := filepath.Dir(tc.testFile)
			fresh := func() string {
				work := filepath.Join(t.TempDir(), "work")
				if err := materializeFixture(work, probe.Fixture); err != nil {
					t.Fatal(err)
				}
				return work
			}

			added := fresh()
			if ok, detail := runCheck(added, checkSpec{Name: "reference", Run: probe.Reference}, 3*time.Minute); !ok {
				t.Fatalf("reference solution failed: %s", detail)
			}
			mustWrite(t, filepath.Join(added, tc.testFile), original+"\nfunc TestAddedByTheAgent(t *testing.T) {}\n")
			mustWrite(t, filepath.Join(added, dir, "extra_test.go"), pkgLine+"\n\nimport \"testing\"\n\nfunc TestInANewFile(t *testing.T) {}\n")
			if ok, detail := runCheck(added, check, checkTimeout); !ok {
				t.Errorf("a correct fix plus added tests failed %q: %s", check.Name, detail)
			}

			if !tc.untouchedFails {
				return
			}
			replaced := fresh()
			mustWrite(t, filepath.Join(replaced, tc.testFile), pkgLine+"\n\nimport \"testing\"\n\nfunc TestNothing(t *testing.T) {}\n")
			if ok, _ := runCheck(replaced, check, checkTimeout); ok {
				t.Errorf("replacing the original tests with no fix passed %q", check.Name)
			}

			exited := fresh()
			mustWrite(t, filepath.Join(exited, dir, "main_test.go"), pkgLine+"\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestMain(m *testing.M) { os.Exit(0) }\n")
			if ok, _ := runCheck(exited, check, checkTimeout); ok {
				t.Errorf("a new TestMain that exits early passed %q with no fix", check.Name)
			}

			initExit := fresh()
			mustWrite(t, filepath.Join(initExit, dir, "zz_init.go"), pkgLine+"\n\nimport \"os\"\n\nfunc init() { os.Exit(0) }\n")
			if ok, _ := runCheck(initExit, check, checkTimeout); ok {
				t.Errorf("an init that exits before any test runs passed %q with no fix", check.Name)
			}
		})
	}
}

// checkNamed returns the check named name from probe's expectations, or
// fails the test if the task carries no such check.
func checkNamed(t *testing.T, probe probeFile, name string) checkSpec {
	t.Helper()
	for _, c := range probe.Expect.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("task has no %q check", name)
	return checkSpec{}
}

// TestVendorReviewCinderCheckCatchesAnUnamendedAnswer: Cinder's data-deletion
// period is also 30 days, so a check that only looks for the digit "30"
// passes an answer that never noticed the amendment and kept the
// pre-amendment 90-day termination notice. The "cites the amendment" check
// must fail that answer.
func TestVendorReviewCinderCheckCatchesAnUnamendedAnswer(t *testing.T) {
	t.Parallel()
	probe := decodeTaskStrict(t, filepath.Join(promptEvalTasksDir, "vendor-review.yaml"))
	check := checkNamed(t, probe, "cinder row cites the amendment")
	work := filepath.Join(t.TempDir(), "work")
	if err := materializeFixture(work, probe.Fixture); err != nil {
		t.Fatal(err)
	}
	wrong := "| Vendor | Termination notice | Auto-renews | Data retention after termination | Section |\n" +
		"| --- | --- | --- | --- | --- |\n" +
		"| Cinder Analytics | 90 days | Yes, successive 1-year terms | 30 days | Sections 4, 6, 11 |\n"
	mustWrite(t, filepath.Join(work, "REVIEW.md"), wrong)
	if ok, _ := runCheck(work, check, checkTimeout); ok {
		t.Error("check passed a Cinder row with the unamended 90-day notice and no mention of the amendment")
	}
}

// TestVendorReviewShapeChecksCatchProseAnswers: the task asks for a markdown
// table with a Section column, not just the right facts somewhere in the
// file. An answer with every fact correct but written as prose bullets, no
// table at all, must fail the shape checks.
func TestVendorReviewShapeChecksCatchProseAnswers(t *testing.T) {
	t.Parallel()
	probe := decodeTaskStrict(t, filepath.Join(promptEvalTasksDir, "vendor-review.yaml"))
	headerCheck := checkNamed(t, probe, "table header names the Section column")
	rowCheck := checkNamed(t, probe, "each vendor is a table row")
	work := filepath.Join(t.TempDir(), "work")
	if err := materializeFixture(work, probe.Fixture); err != nil {
		t.Fatal(err)
	}
	wrong := "# Vendor contract review\n\n" +
		"Arbor Storage: 60 days notice, auto-renews, 45 days retention (Section 5).\n" +
		"Beacon Mail: 45 days notice, auto-renews, 60 days retention (Section 9).\n" +
		"Cinder Analytics: 30 days notice (amended), auto-renews, 30 days retention (Section 6, amended).\n" +
		"Delta Payments: 30 days notice, no renewal, 30 days retention except legal holds (Section 8).\n" +
		"Ember Search: 15 days notice, auto-renews, 14 days retention (Section 9).\n"
	mustWrite(t, filepath.Join(work, "REVIEW.md"), wrong)
	if ok, _ := runCheck(work, headerCheck, checkTimeout); ok {
		t.Error("the Section-column header check passed a prose answer with no markdown table")
	}
	if ok, _ := runCheck(work, rowCheck, checkTimeout); ok {
		t.Error("the table-row check passed a prose answer with no markdown table")
	}

	// A table whose header never names a Section column, but whose data
	// cells happen to say "Section 5", must still fail the header check: a
	// header check that matches any line starting with '|' would wrongly
	// accept a data row's cell text instead of checking the header itself.
	noSectionHeader := filepath.Join(t.TempDir(), "work")
	if err := materializeFixture(noSectionHeader, probe.Fixture); err != nil {
		t.Fatal(err)
	}
	wrongHeader := "| Vendor | Termination notice | Auto-renews | Data retention |\n" +
		"| --- | --- | --- | --- |\n" +
		"| Arbor Storage | 60 days (Section 5) | Yes | 45 days |\n" +
		"| Beacon Mail | 45 days (Section 9) | Yes | 60 days |\n" +
		"| Cinder Analytics | 30 days (Section 6, amended) | Yes | 30 days |\n" +
		"| Delta Payments | 30 days (Section 5) | No | 30 days |\n" +
		"| Ember Search | 15 days (Section 9) | Yes | 14 days |\n"
	mustWrite(t, filepath.Join(noSectionHeader, "REVIEW.md"), wrongHeader)
	if ok, _ := runCheck(noSectionHeader, headerCheck, checkTimeout); ok {
		t.Error("the header check passed a table whose header has no Section column, only a data cell saying \"Section 5\"")
	}
}

// TestNotesNewsletterSectionChecksCatchWeakAnswers: sections can be empty,
// and the Cold start note can be summarized without saying it was
// superseded (the point of the note). Both new checks must catch those
// weak answers.
func TestNotesNewsletterSectionChecksCatchWeakAnswers(t *testing.T) {
	t.Parallel()
	probe := decodeTaskStrict(t, filepath.Join(promptEvalTasksDir, "notes-newsletter.yaml"))
	emptySectionCheck := checkNamed(t, probe, "every section has text")
	coldStartCheck := checkNamed(t, probe, "cold start section notes it was superseded")

	emptyWork := filepath.Join(t.TempDir(), "work")
	if err := materializeFixture(emptyWork, probe.Fixture); err != nil {
		t.Fatal(err)
	}
	empty := "# Design notes\n\n" +
		"## Retry budget\n\n" +
		"## Shard map\n\n" +
		"The shard layout moves into a small replicated store services watch.\n\n" +
		"## Cold start\n\n" +
		"Superseded by the shard map, so we are not building it.\n\n" +
		"## Audit trail\n\n" +
		"Every admin action now writes an append-only log entry.\n"
	mustWrite(t, filepath.Join(emptyWork, "NEWSLETTER.md"), empty)
	if ok, _ := runCheck(emptyWork, emptySectionCheck, checkTimeout); ok {
		t.Error("check passed a NEWSLETTER.md with an empty Retry budget section")
	}

	noMentionWork := filepath.Join(t.TempDir(), "work")
	if err := materializeFixture(noMentionWork, probe.Fixture); err != nil {
		t.Fatal(err)
	}
	noMention := "# Design notes\n\n" +
		"## Retry budget\n\n" +
		"Clients now get a retry budget capping retries at 10% of calls per minute.\n\n" +
		"## Shard map\n\n" +
		"The shard layout moves into a small replicated store services watch.\n\n" +
		"## Cold start\n\n" +
		"New instances took about four minutes to reach full speed before this change.\n\n" +
		"## Audit trail\n\n" +
		"Every admin action now writes an append-only log entry.\n"
	mustWrite(t, filepath.Join(noMentionWork, "NEWSLETTER.md"), noMention)
	if ok, _ := runCheck(noMentionWork, coldStartCheck, checkTimeout); ok {
		t.Error("check passed a Cold start section that never says it was superseded")
	}

	// The check must find the section the same tolerant way the "sections
	// in index order" check does: a heading line starting with "## " that
	// contains "cold start" anywhere, not only a heading anchored exactly
	// at "## cold start".
	looseHeadingSuperseded := filepath.Join(t.TempDir(), "work")
	if err := materializeFixture(looseHeadingSuperseded, probe.Fixture); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(looseHeadingSuperseded, "NEWSLETTER.md"), "# Design notes\n\n"+
		"## Retry budget\n\nClients now get a retry budget.\n\n"+
		"## Shard map\n\nThe shard layout moves into a replicated store.\n\n"+
		"## Note 3: Cold start\n\nSuperseded by the shard map, so we are not building it.\n\n"+
		"## Audit trail\n\nEvery admin action is logged.\n")
	if ok, detail := runCheck(looseHeadingSuperseded, coldStartCheck, checkTimeout); !ok {
		t.Errorf("check failed a Cold start section under a loosely worded heading that does say it was superseded: %s", detail)
	}

	looseHeadingNoMention := filepath.Join(t.TempDir(), "work")
	if err := materializeFixture(looseHeadingNoMention, probe.Fixture); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(looseHeadingNoMention, "NEWSLETTER.md"), "# Design notes\n\n"+
		"## Retry budget\n\nClients now get a retry budget.\n\n"+
		"## Shard map\n\nThe shard layout moves into a replicated store.\n\n"+
		"## Note 3: Cold start\n\nNew instances took about four minutes to reach full speed.\n\n"+
		"## Audit trail\n\nEvery admin action is logged.\n")
	if ok, _ := runCheck(looseHeadingNoMention, coldStartCheck, checkTimeout); ok {
		t.Error("check passed a loosely headed Cold start section that never says it was superseded")
	}
}

func failingChecks(workDir string, checks []checkSpec) []string {
	var names []string
	for _, c := range checks {
		if ok, _ := runCheck(workDir, c, checkTimeout); !ok {
			names = append(names, c.Name)
		}
	}
	return names
}
