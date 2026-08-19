package testaudit

import (
	"strings"
	"testing"
)

func TestExtractFuncBody(t *testing.T) {
	src := `package p

func Before() { _ = "no" }

func Target(t *testing.T) {
	srv := New(func(w W, r R) {
		<-r.Context().Done()
	})
	_ = srv
}

func After() { _ = "no" }
`

	body, ok := extractFuncBody(src, "Target")
	if !ok {
		t.Fatalf("Target not found")
	}
	for _, want := range []string{"<-r.Context().Done()", "_ = srv"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	for _, reject := range []string{"Before", "After", `"no"`} {
		if strings.Contains(body, reject) {
			t.Errorf("body leaked neighboring code %q:\n%s", reject, body)
		}
	}

	if _, ok := extractFuncBody(src, "Missing"); ok {
		t.Errorf("extractFuncBody found a function that does not exist")
	}
}

func TestSnippetAround(t *testing.T) {
	body := "\tfirst line\n\ttime.Sleep(5 * time.Second)\n\tlast line\n"
	got := snippetAround(body, "time.Sleep(")
	if got != "time.Sleep(5 * time.Second)" {
		t.Errorf("snippetAround = %q", got)
	}
	if snippetAround(body, "absent") != "absent" {
		t.Errorf("snippetAround should echo a needle it cannot find")
	}
}
