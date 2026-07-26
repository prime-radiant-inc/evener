package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"
)

func FuzzFieldsOf(f *testing.F) {
	for _, seed := range []uint8{0, 1, 2} {
		f.Add(seed)
	}
	type sample struct {
		Visible string `json:"visible,omitempty"`
		Ignored string `json:"-"`
		Default int
	}
	f.Fuzz(func(t *testing.T, which uint8) {
		var typ reflect.Type
		switch which % 3 {
		case 0:
		case 1:
			typ = reflect.TypeFor[int]()
		default:
			typ = reflect.TypeFor[sample]()
		}
		fields := fieldsOf(typ)
		if typ != nil && typ.Kind() == reflect.Struct && len(fields) != 2 {
			t.Fatalf("fieldsOf returned %+v", fields)
		}
	})
}

func TestRun(t *testing.T) {
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stderr.Close() })
	if got := run(nil, stderr, os.WriteFile); got != 2 {
		t.Fatalf("missing out code=%d", got)
	}
	if got := run([]string{"-bad"}, stderr, os.WriteFile); got != 2 {
		t.Fatalf("bad flag code=%d", got)
	}
	out := filepath.Join(t.TempDir(), "protocol.md")
	if got := run([]string{"-out", out}, stderr, os.WriteFile); got != 0 {
		t.Fatalf("success code=%d", got)
	}
	data, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(data), "AppWire") {
		t.Fatalf("output err=%v", err)
	}
	if got := run([]string{"-out", out}, stderr, func(string, []byte, os.FileMode) error { return errors.New("disk") }); got != 1 {
		t.Fatalf("write failure code=%d", got)
	}
}

func TestMainAndRenderFailure(t *testing.T) {
	oldExit, oldArgs, oldExecute := exitProcess, os.Args, executeTemplate
	t.Cleanup(func() { exitProcess, os.Args, executeTemplate = oldExit, oldArgs, oldExecute })
	os.Args = []string{"appwiredoc"}
	var code int
	exitProcess = func(got int) { code = got }
	main()
	if code != 2 {
		t.Fatalf("main exit=%d", code)
	}
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	executeTemplate = func(*template.Template, *strings.Builder, docData) error { return errors.New("render") }
	if got := run([]string{"-out", "ignored"}, stderr, os.WriteFile); got != 1 {
		t.Fatalf("render code=%d", got)
	}
}

func TestRegisterType(t *testing.T) {
	types := map[string]typeView{}
	if got := registerType(types, nil); got != "(inline)" {
		t.Fatalf("nil=%q", got)
	}
	value := struct{ Field string }{}
	if got := registerType(types, &value); !strings.HasPrefix(got, "struct {") {
		t.Fatalf("unnamed=%q", got)
	}
	_ = registerType(types, &value)
}

func TestBuild(t *testing.T) {
	d := build()
	if len(d.Methods) == 0 || len(d.Notifications) == 0 || len(d.Types) == 0 {
		t.Fatalf("incomplete document data: %+v", d)
	}
	for i := 1; i < len(d.Types); i++ {
		if d.Types[i-1].Name > d.Types[i].Name {
			t.Fatalf("types are not sorted: %q before %q", d.Types[i-1].Name, d.Types[i].Name)
		}
	}
}
