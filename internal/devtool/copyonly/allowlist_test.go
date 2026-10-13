package copyonly

import (
	"reflect"
	"strings"
	"testing"
)

func TestReadAllowlist(t *testing.T) {
	t.Parallel()
	got, err := ReadAllowlist(strings.NewReader("file\ttest\treason\n" +
		"a_test.go\tTestShowsUsage\tModel-facing text (deferred).\n" +
		"\n" +
		"b.test.ts\tshows the title\tFalse positive: fixture text.\n"))
	if err != nil {
		t.Fatalf("ReadAllowlist: %v", err)
	}
	want := []Allowed{
		{File: "a_test.go", Test: "TestShowsUsage", Reason: "Model-facing text (deferred)."},
		{File: "b.test.ts", Test: "shows the title", Reason: "False positive: fixture text."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadAllowlist = %+v, want %+v", got, want)
	}
}

func TestReadAllowlistRefusesMalformedEntries(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"no header":      "a_test.go\tTestShowsUsage\treason\n",
		"wrong header":   "file\tline\ttest\n",
		"missing reason": "file\ttest\treason\na_test.go\tTestShowsUsage\t \n",
		"two columns":    "file\ttest\treason\na_test.go\tTestShowsUsage\n",
		"four columns":   "file\ttest\treason\na_test.go\tTestShowsUsage\twhy\t12\n",
		"duplicate": "file\ttest\treason\na_test.go\tTestShowsUsage\twhy\n" +
			"a_test.go\tTestShowsUsage\twhy again\n",
	} {
		if _, err := ReadAllowlist(strings.NewReader(body)); err == nil {
			t.Errorf("%s: ReadAllowlist accepted %q", name, body)
		}
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()
	targets := []Target{
		{File: "a_test.go", Test: "TestShowsUsage", Line: 10},
		{File: "b.test.ts", Test: "shows the title", Line: 3},
		{File: "b.test.ts", Test: "shows the title", Line: 30}, // same name in two describe blocks
		{File: "c_test.go", Test: "TestNewCopyOnly", Line: 7},
	}
	allowed := []Allowed{
		{File: "a_test.go", Test: "TestShowsUsage", Reason: "deferred"},
		{File: "b.test.ts", Test: "shows the title", Reason: "false positive"},
		{File: "a_test.go", Test: "TestConvertedSince", Reason: "deferred"},
		{File: "c_test.go", Test: "TestShowsUsage", Reason: "keyed by file as well as name"},
	}
	unlisted, stale := Compare(targets, allowed)
	if want := []Target{{File: "c_test.go", Test: "TestNewCopyOnly", Line: 7}}; !reflect.DeepEqual(unlisted, want) {
		t.Errorf("unlisted = %+v, want %+v", unlisted, want)
	}
	if want := allowed[2:]; !reflect.DeepEqual(stale, want) {
		t.Errorf("stale = %+v, want %+v", stale, want)
	}
}
