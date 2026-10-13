package copyonly

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

const inventorySrc = `package x

func TestRejectsEmptyName(t *testing.T) {
	_, err := create("")
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatal(err)
	}
}

func TestHelpShowsUsage(t *testing.T) {
	out := help()
	if !strings.Contains(out, "Usage: evener") {
		t.Fatal(out)
	}
}

func TestStatusIsRunning(t *testing.T) {
	if status() != "running" || label() != "only in this test" {
		t.Fatal("status")
	}
}

func TestPromptMentionsTools(t *testing.T) {
	if !regexp.MustCompile(` + "`use the tools \\w+`" + `).MatchString(prompt()) {
		t.Fatal("prompt")
	}
}

func TestRendersTheRetryLabel(t *testing.T) {
	if render() != "Retry later" {
		t.Fatal("label")
	}
}

func TestRetryKeepsItsValue(t *testing.T) {
	if render() != "Retry later" {
		t.Fatal("label")
	}
}

func assertGreets(t *testing.T, out string) {
	if "Hello there" != out {
		t.Fatal(out)
	}
}
`

func TestGoInventory(t *testing.T) {
	t.Parallel()
	s := newScanner("name is required\nUsage: evener\nrunning\nRetry later\nHello there", "Always use the tools you have.")
	got, err := s.scanGo("pkg/x_test.go", inventorySrc, true)
	if err != nil {
		t.Fatalf("scanGo: %v", err)
	}
	wantHits := []Hit{
		{File: "pkg/x_test.go", Line: 5, Category: CategoryGoErrString, Literal: "name is required"},
		{File: "pkg/x_test.go", Line: 12, Category: CategoryGoCopy, Literal: "Usage: evener"},
		{File: "pkg/x_test.go", Line: 24, Category: CategoryGoAsset, Literal: "use the tools"},
		{File: "pkg/x_test.go", Line: 30, Category: CategoryGoCopy, Literal: "Retry later"},
		{File: "pkg/x_test.go", Line: 36, Category: CategoryGoCopy, Literal: "Retry later"},
		{File: "pkg/x_test.go", Line: 42, Category: CategoryGoCopy, Literal: "Hello there"},
	}
	if !reflect.DeepEqual(got.hits, wantHits) {
		t.Errorf("hits = %+v\nwant %+v", got.hits, wantHits)
	}
	// "running" is contract-shaped and not counted; "only in this test" is
	// fixture data the test fed in.
	if got.fixtures != 1 {
		t.Errorf("fixtures = %d, want 1", got.fixtures)
	}
	wantCopyOnly := []CopyOnlyTest{
		{File: "pkg/x_test.go", Line: 10, Test: "TestHelpShowsUsage", Purpose: PurposeCopy},
		{File: "pkg/x_test.go", Line: 23, Test: "TestPromptMentionsTools", Purpose: PurposeCopy},
		{File: "pkg/x_test.go", Line: 29, Test: "TestRendersTheRetryLabel", Purpose: PurposeCopy},
		{File: "pkg/x_test.go", Line: 35, Test: "TestRetryKeepsItsValue", Purpose: PurposeBehavior},
	}
	if !reflect.DeepEqual(got.copyOnly, wantCopyOnly) {
		t.Errorf("copy-only = %+v\nwant %+v", got.copyOnly, wantCopyOnly)
	}
}

func testReport() Report {
	return Report{
		Hits: []Hit{
			{File: "agent/b_test.go", Line: 9, Category: CategoryGoCopy, Literal: "Usage: evener"},
			{File: "agent/b_test.go", Line: 3, Category: CategoryGoErrString, Literal: "name\tis\nrequired"},
			{File: "cmd/evener-hub/frontend/src/a.test.tsx", Line: 4, Category: CategoryTSAssert, Literal: "No sessions yet"},
			{File: "cmd/evener-hub/frontend/src/a.test.tsx", Line: 5, Category: CategoryTSLocator, Literal: "Retry"},
			{File: "cmd/evener-hub/frontend/src/a.test.tsx", Line: 6, Category: CategoryTSConstant, Literal: "EMPTY_MESSAGE"},
			{File: "llm/c_test.go", Line: 1, Category: CategoryGoAsset, Literal: strings.Repeat("long phrase ", 20)},
		},
		Fixtures: map[string]int{"agent": 2, "cmd/evener-hub/frontend/src": 1, "other": 4},
		CopyOnly: []CopyOnlyTest{
			{File: "agent/b_test.go", Line: 1, Test: "TestShowsUsage", Purpose: PurposeCopy},
			{File: "agent/b_test.go", Line: 8, Test: "TestRetries", Purpose: PurposeBehavior},
			{File: "cmd/evener-hub/frontend/src/a.test.tsx", Line: 3, Test: "shows the title", Purpose: PurposeCopy},
			{File: "cmd/evener-tui/x_test.go", Line: 2, Test: "TestSaysHi", Purpose: PurposeCopy},
			{File: "hooks_test.go", Line: 7, Test: "TestHookRuns", Purpose: PurposeBehavior},
		},
	}
}

func TestWritePackageTable(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	WritePackageTable(&out, testReport(), 0)
	want := "| package | total | ts-assert | ts-locator | ts-constant | go-errstring | go-copy | go-asset | fixture (excluded) |\n" +
		"|---|---:|---:|---:|---:|---:|---:|---:|---:|\n" +
		"| `cmd/evener-hub/frontend/src` | 3 | 1 | 1 | 1 |  |  |  | 1 |\n" +
		"| `agent` | 2 |  |  |  | 1 | 1 |  | 2 |\n" +
		"| `llm` | 1 |  |  |  |  |  | 1 |  |\n" +
		"| **all (3 packages)** | **6** | **1** | **1** | **1** | **1** | **1** | **1** | **7** |\n"
	if out.String() != want {
		t.Errorf("table:\n%s\nwant:\n%s", out.String(), want)
	}

	out.Reset()
	WritePackageTable(&out, testReport(), 1)
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 4 || !strings.Contains(lines[2], "frontend/src") {
		t.Errorf("--top 1 table:\n%s", out.String())
	}
}

func TestWriteHitsTSV(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	WriteHitsTSV(&out, testReport().Hits)
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	want := []string{
		"file\tline\tcategory\tliteral",
		"agent/b_test.go\t3\tgo-errstring\tname is required",
		"agent/b_test.go\t9\tgo-copy\tUsage: evener",
		"cmd/evener-hub/frontend/src/a.test.tsx\t4\tts-assert\tNo sessions yet",
		"cmd/evener-hub/frontend/src/a.test.tsx\t5\tts-locator\tRetry",
		"cmd/evener-hub/frontend/src/a.test.tsx\t6\tts-constant\tEMPTY_MESSAGE",
		"llm/c_test.go\t1\tgo-asset\t" + strings.Repeat("long phrase ", 20)[:160],
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("hits TSV:\n%q\nwant:\n%q", lines, want)
	}
}

func TestWriteCopyOnly(t *testing.T) {
	t.Parallel()
	var table, tsv bytes.Buffer
	WriteCopyOnly(&table, &tsv, testReport().CopyOnly)
	wantTable := "| area | copy-only (targets) | behavior, copy-only oracle (incidental) |\n" +
		"|---|---:|---:|\n" +
		"| `agent` | 1 | 1 |\n" +
		"| `cmd/evener-hub/frontend` | 1 |  |\n" +
		"| `cmd/evener-tui` | 1 |  |\n" +
		"| `hooks_test.go` |  | 1 |\n" +
		"| **all** | **3** | **2** |\n"
	if table.String() != wantTable {
		t.Errorf("table:\n%s\nwant:\n%s", table.String(), wantTable)
	}
	wantTSV := "file\tline\ttest\tpurpose\n" +
		"agent/b_test.go\t1\tTestShowsUsage\tcopy\n" +
		"agent/b_test.go\t8\tTestRetries\tbehavior\n" +
		"cmd/evener-hub/frontend/src/a.test.tsx\t3\tshows the title\tcopy\n" +
		"cmd/evener-tui/x_test.go\t2\tTestSaysHi\tcopy\n" +
		"hooks_test.go\t7\tTestHookRuns\tbehavior\n"
	if tsv.String() != wantTSV {
		t.Errorf("TSV:\n%s\nwant:\n%s", tsv.String(), wantTSV)
	}
}

func TestMergeReports(t *testing.T) {
	t.Parallel()
	a := Report{Hits: []Hit{{File: "a", Line: 1}}, Fixtures: map[string]int{"p": 1, "q": 2}, CopyOnly: []CopyOnlyTest{{File: "a"}}}
	b := Report{Hits: []Hit{{File: "b", Line: 1}}, Fixtures: map[string]int{"p": 3}, CopyOnly: []CopyOnlyTest{{File: "b"}}}
	got := Merge(a, b)
	if len(got.Hits) != 2 || len(got.CopyOnly) != 2 || !reflect.DeepEqual(got.Fixtures, map[string]int{"p": 4, "q": 2}) {
		t.Errorf("Merge = %+v", got)
	}
}

// TestGoInventoryReadsAConcatenationsLeadingLiteral pins that a check against
// "copy "+value compares output with the copy in front.
func TestGoInventoryReadsAConcatenationsLeadingLiteral(t *testing.T) {
	t.Parallel()
	src := `package x

func TestShowsTheMkdirFailure(t *testing.T) {
	err := save(dir)
	if !strings.Contains(err.Error(), "launchconfig: mkdir "+dir+" failed") {
		t.Fatal(err)
	}
}

func TestSendsTheToken(t *testing.T) {
	if got := header(); got != ("Bearer " + token) {
		t.Fatal(got)
	}
}
`
	s := newScanner("launchconfig: mkdir %s\nBearer", "")
	got, err := s.scanGo("pkg/x_test.go", src, true)
	if err != nil {
		t.Fatalf("scanGo: %v", err)
	}
	wantHits := []Hit{
		{File: "pkg/x_test.go", Line: 5, Category: CategoryGoErrString, Literal: "launchconfig: mkdir"},
		{File: "pkg/x_test.go", Line: 11, Category: CategoryGoCopy, Literal: "Bearer"},
	}
	if !reflect.DeepEqual(got.hits, wantHits) {
		t.Errorf("hits = %+v\nwant %+v", got.hits, wantHits)
	}
	wantCopyOnly := []CopyOnlyTest{
		{File: "pkg/x_test.go", Line: 3, Test: "TestShowsTheMkdirFailure", Purpose: PurposeCopy},
		{File: "pkg/x_test.go", Line: 10, Test: "TestSendsTheToken", Purpose: PurposeBehavior},
	}
	if !reflect.DeepEqual(got.copyOnly, wantCopyOnly) {
		t.Errorf("copy-only = %+v\nwant %+v", got.copyOnly, wantCopyOnly)
	}
}
