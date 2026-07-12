package main

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/mod/modfile"
)

type registryErrorWriter struct{}

func (registryErrorWriter) Write([]byte) (int, error) { return 0, errors.New("write") }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read") }

func scenarioRunRegistryAllPipelineFailures(t *testing.T) {
	var out, errOut bytes.Buffer
	if runRegistry([]string{"--bad"}, &out, &errOut) != 1 || runRegistry(nil, &out, &errOut) != 1 {
		t.Fatal("argument failures")
	}
	if runRegistry([]string{"--registry", "x", "extra"}, &out, &errOut) != 1 {
		t.Fatal("extra arg")
	}
	if runRegistry([]string{"--registry", filepath.Join(t.TempDir(), "missing")}, &out, &errOut) != 1 {
		t.Fatal("open")
	}
	bad := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(bad, []byte("bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runRegistry([]string{"--registry", bad}, &out, &errOut) != 1 {
		t.Fatal("parse")
	}
	reg := filepath.Join(t.TempDir(), "registry")
	if err := os.WriteFile(reg, []byte("native:.:.:FuzzX\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := discoverWorkspace
	t.Cleanup(func() { discoverWorkspace = old })
	discoverWorkspace = func(string) ([]Target, error) { return nil, errors.New("discover") }
	if runRegistry([]string{"--registry", reg, "--check"}, &out, &errOut) != 1 {
		t.Fatal("discover")
	}
	discoverWorkspace = func(string) ([]Target, error) { return nil, nil }
	if runRegistry([]string{"--registry", reg, "--check"}, &out, &errOut) != 1 {
		t.Fatal("drift")
	}
	discoverWorkspace = func(string) ([]Target, error) {
		return []Target{{Kind: "native", Module: ".", Package: ".", Name: "FuzzX"}}, nil
	}
	if runRegistry([]string{"--registry", reg, "--emit-plan"}, registryErrorWriter{}, &errOut) != 1 {
		t.Fatal("emit")
	}
	if runRegistry([]string{"--registry", reg, "--emit-plan"}, &out, &errOut) != 0 {
		t.Fatalf("success: %s", errOut.String())
	}
}

func scenarioRegistryMainAndReaderWriterErrors(t *testing.T) {
	if _, err := ParseRegistry(failingReader{}); err == nil {
		t.Fatal("reader error")
	}
	if err := EmitPlan(registryErrorWriter{}, []Target{{Kind: "native", Module: ".", Package: ".", Name: "F"}}); err == nil {
		t.Fatal("writer error")
	}
	oldExit, oldArgs := osExit, os.Args
	t.Cleanup(func() { osExit, os.Args = oldExit, oldArgs })
	os.Args = []string{"serf-fuzzregistry"}
	got := -1
	osExit = func(code int) { got = code }
	main()
	if got != 1 {
		t.Fatalf("exit=%d", got)
	}
}

func scenarioRegistryFocusAndEmitValidationFailures(t *testing.T) {
	var out, errOut bytes.Buffer
	reg := filepath.Join(t.TempDir(), "registry")
	row := "native:.:.:FuzzX::missing.go#Nope\n"
	if err := os.WriteFile(reg, []byte(row), 0o600); err != nil {
		t.Fatal(err)
	}
	old := discoverWorkspace
	t.Cleanup(func() { discoverWorkspace = old })
	discoverWorkspace = func(string) ([]Target, error) {
		return []Target{{Kind: "native", Module: ".", Package: ".", Name: "FuzzX", Focus: "missing.go#Nope"}}, nil
	}
	if got := runRegistry([]string{"--registry", reg, "--check", "--repo-root", t.TempDir()}, &out, &errOut); got != 1 {
		t.Fatalf("focus=%d", got)
	}
	if err := EmitPlan(&out, []Target{{Kind: "bad", Module: ".", Package: ".", Name: "x"}}); err == nil {
		t.Fatal("invalid emit")
	}
	dup := Target{Kind: "native", Module: ".", Package: ".", Name: "x"}
	if err := EmitPlan(&out, []Target{dup, dup}); err == nil {
		t.Fatal("duplicate emit")
	}
}

func scenarioCanonicalAndHelperRejections(t *testing.T) {
	for _, target := range []Target{
		{Kind: "bad", Module: ".", Package: ".", Name: "x"}, {Kind: "native", Module: "", Package: ".", Name: "x"},
		{Kind: "native", Module: "../x", Package: ".", Name: "x"}, {Kind: "native", Module: ".", Package: "", Name: "x"},
		{Kind: "native", Module: ".", Package: "../x", Name: "x"}, {Kind: "native", Module: ".", Package: ".", Name: ""},
		{Kind: "native", Module: ".", Package: ".", Name: "x:y"},
	} {
		if _, err := canonicalTarget(target); err == nil {
			t.Errorf("accepted %+v", target)
		}
	}
	if got, err := canonicalModule("./x"); err != nil || got != "x" {
		t.Fatalf("module=%q %v", got, err)
	}
	if got, err := canonicalPackage("./x"); err != nil || got != "./x" {
		t.Fatalf("pkg=%q %v", got, err)
	}
	if _, err := packagePath("/a/b", "/a/c"); err == nil {
		t.Fatal("outside package")
	}
	for _, n := range []string{"testdata", "vendor", "node_modules", ".hidden", "_hidden"} {
		if !skipDirectory(n) {
			t.Errorf("not skipped %q", n)
		}
	}
	set, issues := targetSet("x", []Target{{Kind: "test", Module: ".", Package: ".", Name: "T"}, {Kind: "bad", Module: ".", Package: ".", Name: "T"}})
	if len(set) != 0 || len(issues) != 1 {
		t.Fatalf("set=%v issues=%v", set, issues)
	}
	if pathWithinDir("/a", "/b") {
		t.Fatal("outside accepted")
	}
}

func scenarioASTHelperBranches(t *testing.T) {
	src := `package p
import r "pgregory.net/rapid"
import _ "pgregory.net/rapid"
import . "pgregory.net/rapid"
// unrelated
// serf:fuzz rapid
func TestMarked(t *T) { other(); x.Check(); r.Check(t, func(t *r.T) {}) }
func TestNoBody(t *T)
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	names := rapidImportNames(f)
	if _, ok := names["r"]; !ok || len(names) != 1 {
		t.Fatalf("names=%v", names)
	}
	var marked, noBody *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok {
			if fn.Name.Name == "TestMarked" {
				marked = fn
			} else if fn.Name.Name == "TestNoBody" {
				noBody = fn
			}
		}
	}
	if !hasRapidMarker(fset, marked) || hasRapidMarker(fset, noBody) {
		t.Fatal("markers")
	}
	if !callsRapidCheck(marked, names) || callsRapidCheck(noBody, names) || callsRapidCheck(marked, nil) {
		t.Fatal("rapid calls")
	}
	marked.Doc = &ast.CommentGroup{List: []*ast.Comment{{Slash: marked.Pos(), Text: "// no marker"}}}
	if hasRapidMarker(fset, marked) {
		t.Fatal("unexpected marker")
	}
	weird := &ast.FuncDecl{Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: &ast.CallExpr{Fun: &ast.SelectorExpr{X: &ast.CallExpr{Fun: ast.NewIdent("get")}, Sel: ast.NewIdent("Check")}}}}}}
	if callsRapidCheck(weird, names) {
		t.Fatal("non-ident receiver")
	}
	if displayPath("\x00", "x") == "" {
		t.Fatal("display")
	}
	targets := []Target{{Module: "m", Package: "p", Kind: "rapid", Name: "b"}, {Module: "m", Package: "p", Kind: "native", Name: "c"}, {Module: "m", Package: "p", Kind: "native", Name: "a"}}
	sortTargets(targets)
	if targets[0].Name != "a" {
		t.Fatalf("sort=%v", targets)
	}
}

func scenarioRegistryInjectedFilesystemFailures(t *testing.T) {
	oldWalk, oldPkg, oldRel, oldAbs := registryWalkDir, registryPackagePath, registryRel, registryAbs
	t.Cleanup(func() {
		registryWalkDir, registryPackagePath, registryRel, registryAbs = oldWalk, oldPkg, oldRel, oldAbs
	})
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "m"), 0o700)
	mustRegistryWrite(t, filepath.Join(root, "go.work"), "go 1.25\nuse ./m\n")
	mustRegistryWrite(t, filepath.Join(root, "m", "go.mod"), "module m\ngo 1.25\n")
	boom := errors.New("boom")
	registryWalkDir = func(_ string, fn fs.WalkDirFunc) error { return fn("x", nil, boom) }
	if _, err := DiscoverWorkspace(root); !errors.Is(err, boom) {
		t.Fatalf("walk callback=%v", err)
	}
	file := filepath.Join(root, "m", "x_test.go")
	mustRegistryWrite(t, file, "package p\n")
	info, _ := os.Stat(file)
	entry := fs.FileInfoToDirEntry(info)
	registryWalkDir = func(_ string, fn fs.WalkDirFunc) error { return fn(file, entry, nil) }
	registryPackagePath = func(string, string) (string, error) { return "", boom }
	if _, err := DiscoverWorkspace(root); !errors.Is(err, boom) {
		t.Fatalf("package=%v", err)
	}
	registryRel = func(string, string) (string, error) { return "", boom }
	if pathWithinDir("a", "b") {
		t.Fatal("rel error accepted")
	}
	if _, err := packagePath("a", "b"); !errors.Is(err, boom) {
		t.Fatalf("package rel=%v", err)
	}
	registryAbs = func(string) (string, error) { return "", boom }
	if got := displayPath("a", "b"); got != "b" {
		t.Fatalf("display abs=%q", got)
	}
	registryAbs = oldAbs
	if got := displayPath("a", "b"); got != "b" {
		t.Fatalf("display rel=%q", got)
	}
}

func scenarioReadWorkspaceModuleFailureMatrix(t *testing.T) {
	oldAbs, oldEval, oldRel, oldParse := registryAbs, registryEvalSymlinks, registryRel, registryParseWork
	t.Cleanup(func() {
		registryAbs, registryEvalSymlinks, registryRel, registryParseWork = oldAbs, oldEval, oldRel, oldParse
	})
	boom := errors.New("boom")
	registryAbs = func(string) (string, error) { return "", boom }
	if _, err := readWorkspaceModules("x"); err == nil {
		t.Fatal("abs")
	}
	registryAbs = oldAbs
	registryEvalSymlinks = func(string) (string, error) { return "", boom }
	if _, err := readWorkspaceModules(t.TempDir()); err == nil {
		t.Fatal("root eval")
	}
	registryEvalSymlinks = oldEval
	if _, err := readWorkspaceModules(t.TempDir()); err == nil {
		t.Fatal("missing work")
	}
	bad := t.TempDir()
	mustRegistryWrite(t, filepath.Join(bad, "go.work"), "bad")
	if _, err := readWorkspaceModules(bad); err == nil {
		t.Fatal("bad work")
	}
	empty := t.TempDir()
	mustRegistryWrite(t, filepath.Join(empty, "go.work"), "go 1.25\n")
	if _, err := readWorkspaceModules(empty); err == nil {
		t.Fatal("empty work")
	}
	root := t.TempDir()
	mustRegistryWrite(t, filepath.Join(root, "go.work"), "go 1.25\nuse ./m\n")
	if _, err := readWorkspaceModules(root); err == nil {
		t.Fatal("missing module")
	}
	os.Mkdir(filepath.Join(root, "m"), 0o700)
	if _, err := readWorkspaceModules(root); err == nil {
		t.Fatal("missing go.mod")
	}
	registryRel = func(string, string) (string, error) { return "", boom }
	if _, err := readWorkspaceModules(root); err == nil {
		t.Fatal("rel")
	}
	registryRel = oldRel
	registryEvalSymlinks = func(p string) (string, error) {
		if filepath.Base(p) == "m" {
			return "", boom
		}
		return oldEval(p)
	}
	if _, err := readWorkspaceModules(root); err == nil {
		t.Fatal("module eval")
	}
	registryEvalSymlinks = oldEval
	mustRegistryWrite(t, filepath.Join(root, "m", "go.mod"), "module m\ngo 1.25\n")
	mods, err := readWorkspaceModules(root)
	if err != nil || len(mods) != 1 {
		t.Fatalf("mods=%v err=%v", mods, err)
	}
	registryRel = func(string, string) (string, error) { return "../outside", nil }
	if _, err := readWorkspaceModules(root); err == nil {
		t.Fatal("outside logical")
	}
	registryRel = oldRel
	registryParseWork = func(string, []byte, modfile.VersionFixer) (*modfile.WorkFile, error) {
		return &modfile.WorkFile{Use: []*modfile.Use{{Path: ""}}}, nil
	}
	if _, err := readWorkspaceModules(root); err == nil {
		t.Fatal("empty synthetic use")
	}
	registryParseWork = oldParse
	dot := t.TempDir()
	mustRegistryWrite(t, filepath.Join(dot, "go.work"), "go 1.25\nuse .\n")
	mustRegistryWrite(t, filepath.Join(dot, "go.mod"), "module dot\ngo 1.25\n")
	mods, err = readWorkspaceModules(dot)
	if err != nil || len(mods) != 1 || mods[0].label != "." {
		t.Fatalf("dot mods=%v err=%v", mods, err)
	}
	dup := t.TempDir()
	os.Mkdir(filepath.Join(dup, "m"), 0o700)
	mustRegistryWrite(t, filepath.Join(dup, "m", "go.mod"), "module m\ngo 1.25\n")
	mustRegistryWrite(t, filepath.Join(dup, "go.work"), "go 1.25\nuse (\n ./m\n ./m\n)\n")
	if _, err := readWorkspaceModules(dup); err == nil {
		t.Fatal("duplicate module")
	}
}

func scenarioDiscoverWorkspaceMalformedFilesAndRapidIssues(t *testing.T) {
	makeWS := func(t *testing.T, source string) string {
		t.Helper()
		root := t.TempDir()
		os.Mkdir(filepath.Join(root, "m"), 0o700)
		mustRegistryWrite(t, filepath.Join(root, "go.work"), "go 1.25\nuse ./m\n")
		mustRegistryWrite(t, filepath.Join(root, "m", "go.mod"), "module m\ngo 1.25\n")
		mustRegistryWrite(t, filepath.Join(root, "m", "x_test.go"), source)
		return root
	}
	if _, err := DiscoverWorkspace(makeWS(t, "package p\nfunc (")); err == nil {
		t.Fatal("parse")
	}
	badMarker := `package p
import "testing"
import "pgregory.net/rapid"
// serf:fuzz rapid
func Bad(t *testing.T) {}
func TestUnmarked(t *testing.T) { rapid.Check(t, func(t *rapid.T){}) }
`
	if _, err := DiscoverWorkspace(makeWS(t, badMarker)); err == nil {
		t.Fatal("rapid issues")
	}
	constraint := "//go:build (\n\npackage p\n"
	if _, err := DiscoverWorkspace(makeWS(t, constraint)); err == nil {
		t.Fatal("constraint")
	}
}

func mustRegistryWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

var _ io.Reader = failingReader{}
