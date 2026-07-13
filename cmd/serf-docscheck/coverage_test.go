package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDocsCheckAllPaths(t *testing.T) {
	var out, errOut bytes.Buffer
	check := func(pkg string) ([]violation, error) {
		switch pkg {
		case "clean":
			return nil, nil
		case "bad":
			return []violation{{pkg: "z", file: "z.go", kind: "var", name: "Z"}, {pkg: "a", file: "a.go", kind: "func", name: "B"}, {pkg: "a", file: "a.go", kind: "type", name: "A"}}, nil
		default:
			return nil, errors.New("parse")
		}
	}
	if code := runDocsCheck([]string{"clean"}, check, &out, &errOut); code != 0 || !strings.Contains(out.String(), "all exported") {
		t.Fatalf("clean = %d, %q, %q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := runDocsCheck([]string{"bad"}, check, &out, &errOut); code != 1 || !strings.Contains(out.String(), "a/a.go: type A") || !strings.Contains(errOut.String(), "3 undocumented") {
		t.Fatalf("bad = %d, %q, %q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := runDocsCheck([]string{"broken"}, check, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "broken: parse") {
		t.Fatalf("broken = %d, %q", code, errOut.String())
	}
}

func TestCheckPackageEveryDeclarationKind(t *testing.T) {
	d := t.TempDir()
	src := `package sample
func Exported() {}
func (T) Method() {}
func hidden() {}
type T struct{}
type documented struct{}
// Group docs.
type Grouped struct{}
var ExportedVar, hiddenVar = 1, 2
const ExportedConst = 1
// Spec docs.
var SpecDocumented = 1
`
	if err := os.WriteFile(filepath.Join(d, "sample.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "ignored_test.go"), []byte("package sample\nfunc Ignored() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := checkPackage(d)
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, v := range got {
		joined.WriteString(v.kind)
		joined.WriteString(":")
		joined.WriteString(v.name)
		joined.WriteString(",")
	}
	for _, want := range []string{"func:Exported", "type:T", "var:ExportedVar", "const:ExportedConst"} {
		if !strings.Contains(joined.String(), want) {
			t.Errorf("%q missing %q", joined.String(), want)
		}
	}
	if _, err := checkPackage(filepath.Join(d, "missing")); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestMainUsesExit(t *testing.T) {
	oldExit, oldPkgs := osExit, libraryPackages
	t.Cleanup(func() { osExit, libraryPackages = oldExit, oldPkgs })
	libraryPackages = nil
	got := -1
	osExit = func(code int) { got = code }
	main()
	if got != 0 {
		t.Fatalf("exit = %d", got)
	}
}
