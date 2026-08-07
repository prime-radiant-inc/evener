package promoter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type faultFile struct {
	name     string
	writeErr error
	closeErr error
}

func (f *faultFile) Name() string              { return f.name }
func (f *faultFile) Write([]byte) (int, error) { return 0, f.writeErr }
func (f *faultFile) Close() error              { return f.closeErr }

func defaultBucketOps() bucketStoreOps {
	return bucketStoreOps{
		readFile: os.ReadFile, mkdirAll: os.MkdirAll, createTemp: func(dir, pattern string) (atomicFile, error) {
			return os.CreateTemp(dir, pattern)
		},
		rename: os.Rename, remove: os.Remove,
	}
}

func FuzzBucketStorePersistence(f *testing.F) {
	for mode := range 12 {
		f.Add(byte(mode))
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "state", "buckets.json")
		sig := Signature{Oracle: Invariant, Key: "stable"}
		ops := defaultBucketOps()

		switch mode % 12 {
		case 0:
			store, err := openBucketStoreWithOps(path, ops)
			if err != nil || store.Len() != 0 {
				t.Fatalf("new store = (%v, %v), want empty", store, err)
			}
		case 1:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			store, err := openBucketStoreWithOps(path, ops)
			if err != nil || store.Len() != 0 {
				t.Fatalf("empty store: %v", err)
			}
		case 2:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("null"), 0o644); err != nil {
				t.Fatal(err)
			}
			store, err := openBucketStoreWithOps(path, ops)
			if err != nil || store.Len() != 0 {
				t.Fatalf("null store: %v", err)
			}
		case 3:
			ops.readFile = func(string) ([]byte, error) { return nil, errors.New("read fault") }
			_, err := openBucketStoreWithOps(path, ops)
			if err == nil || !strings.Contains(err.Error(), "read bucket store") {
				t.Fatalf("err = %v", err)
			}
		case 4:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := openBucketStoreWithOps(path, ops)
			if err == nil || !strings.Contains(err.Error(), "parse bucket store") {
				t.Fatalf("err = %v", err)
			}
		default:
			store, err := openBucketStoreWithOps(path, ops)
			if err != nil {
				t.Fatal(err)
			}
			switch mode % 12 {
			case 5:
				store.ops.mkdirAll = func(string, os.FileMode) error { return errors.New("mkdir fault") }
			case 6:
				store.ops.createTemp = func(string, string) (atomicFile, error) { return nil, errors.New("create fault") }
			case 7:
				store.ops.createTemp = func(dir, _ string) (atomicFile, error) {
					return &faultFile{name: filepath.Join(dir, "tmp"), writeErr: errors.New("write fault")}, nil
				}
			case 8:
				store.ops.createTemp = func(dir, _ string) (atomicFile, error) {
					return &faultFile{name: filepath.Join(dir, "tmp"), closeErr: errors.New("close fault")}, nil
				}
			case 9:
				store.ops.rename = func(string, string) error { return errors.New("rename fault") }
			case 10:
				if err := store.Add(sig, "regression_test.go"); err != nil {
					t.Fatal(err)
				}
				reopened, err := OpenBucketStore(path)
				if err != nil {
					t.Fatal(err)
				}
				if got, ok := reopened.Get(sig); !ok || got != "regression_test.go" {
					t.Fatalf("record = %q, %v", got, ok)
				}
				return
			case 11:
				if store.Has(sig) {
					t.Fatal("unexpected bucket")
				}
				return
			}
			if err := store.Add(sig, "regression_test.go"); err == nil {
				t.Fatal("injected persistence fault succeeded")
			}
		}
	})
}

func FuzzGoTestEmission(f *testing.F) {
	for mode := range 8 {
		f.Add(byte(mode))
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		g := GoTest{Package: "regressions", Surface: "9/a", Oracle: Panic, Hash: "abc", ReplayBody: "\t_ = t"}
		switch mode % 8 {
		case 0:
			g.Package = ""
			if _, err := RenderGoTest(g); err == nil {
				t.Fatal("empty package accepted")
			}
		case 1:
			g.Hash = ""
			if _, err := RenderGoTest(g); err == nil {
				t.Fatal("empty hash accepted")
			}
		case 2:
			g.ReplayBody = "not valid Go ("
			if _, err := RenderGoTest(g); err == nil {
				t.Fatal("invalid body accepted")
			}
		case 3:
			if got := sanitizeIdent(""); got != "x" {
				t.Fatalf("sanitize empty = %q", got)
			}
			if got := sanitizeIdent("9/a"); got != "x9_a" {
				t.Fatalf("sanitize = %q", got)
			}
		case 4:
			_, err := writeGoTestWithOps(t.TempDir(), g, goTestOps{mkdirAll: func(string, os.FileMode) error { return errors.New("mkdir") }, writeFile: os.WriteFile})
			if err == nil || !strings.Contains(err.Error(), "create output dir") {
				t.Fatalf("err = %v", err)
			}
		case 5:
			_, err := writeGoTestWithOps(t.TempDir(), g, goTestOps{mkdirAll: os.MkdirAll, writeFile: func(string, []byte, os.FileMode) error { return errors.New("write") }})
			if err == nil || !strings.Contains(err.Error(), "write regression test") {
				t.Fatalf("err = %v", err)
			}
		case 6:
			g.Package = ""
			if _, err := WriteGoTest(t.TempDir(), g); err == nil {
				t.Fatal("render failure not propagated")
			}
		case 7:
			path, err := WriteGoTest(t.TempDir(), g)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
		}
	})
}

type errorAdapter struct{ emitErr error }

func (a errorAdapter) Minimize(f Failure) Failure                 { f.Detail = "minimized"; return f }
func (errorAdapter) Signature(Failure) Signature                  { return Signature{Oracle: Invariant, Key: "key"} }
func (errorAdapter) Replay(context.Context, Failure) (bool, bool) { return true, true }
func (a errorAdapter) Emit(Failure) (string, error)               { return "regression_test.go", a.emitErr }

type errorQuarantiner struct{ err error }

func (q errorQuarantiner) Quarantine(Failure, int) error { return q.err }

func FuzzPromoterOutcomes(f *testing.F) {
	for mode := range 8 {
		f.Add(byte(mode))
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		wantStrings := []string{"promoted", "already-known", "quarantined", "outcome(0)"}
		if got := []string{Promoted.String(), AlreadyKnown.String(), Quarantined.String(), Outcome(0).String()}; fmt.Sprint(got) != fmt.Sprint(wantStrings) {
			t.Fatalf("outcomes = %v", got)
		}
		dir := t.TempDir()
		store, err := OpenBucketStore(filepath.Join(dir, "buckets.json"))
		if err != nil {
			t.Fatal(err)
		}
		adapter := errorAdapter{}
		p := New(adapter, store, errorQuarantiner{}, 0)
		if p.K != 5 {
			t.Fatalf("default K = %d", p.K)
		}
		switch mode % 8 {
		case 0:
			adapter.emitErr = errors.New("emit")
			p.adapter = adapter
			if _, err := p.Promote(context.Background(), Failure{}); err == nil || !strings.Contains(err.Error(), "emit regression test") {
				t.Fatalf("err = %v", err)
			}
		case 1:
			store.ops.mkdirAll = func(string, os.FileMode) error { return errors.New("record") }
			if _, err := p.Promote(context.Background(), Failure{}); err == nil || !strings.Contains(err.Error(), "record bucket") {
				t.Fatalf("err = %v", err)
			}
		case 2:
			p.adapter = &testAdapter{replay: func(int) (bool, bool) { return false, false }}
			p.log = errorQuarantiner{err: errors.New("quarantine")}
			if out, err := p.Promote(context.Background(), Failure{}); out != Quarantined || err == nil {
				t.Fatalf("result = %v, %v", out, err)
			}
		case 3:
			if out, err := p.Promote(context.Background(), Failure{}); out != Promoted || err != nil {
				t.Fatalf("result = %v, %v", out, err)
			}
		case 4:
			if err := store.Add(adapter.Signature(Failure{}), "known_test.go"); err != nil {
				t.Fatal(err)
			}
			if out, err := p.Promote(context.Background(), Failure{}); out != AlreadyKnown || err != nil {
				t.Fatalf("result = %v, %v", out, err)
			}
		case 5:
			p.adapter = &testAdapter{replay: func(int) (bool, bool) { return false, true }}
			if out, err := p.Promote(context.Background(), Failure{}); out != Quarantined || err != nil {
				t.Fatalf("result = %v, %v", out, err)
			}
		case 6:
			p.adapter = &testAdapter{replay: func(int) (bool, bool) { return true, false }}
			if out, err := p.Promote(context.Background(), Failure{}); out != Quarantined || err != nil {
				t.Fatalf("result = %v, %v", out, err)
			}
		case 7:
			p.adapter = &testAdapter{replay: func(int) (bool, bool) { return true, true }, pkg: "regressions", dir: dir}
			if out, err := p.Promote(context.Background(), deterministicFailure()); out != Promoted || err != nil {
				t.Fatalf("result = %v, %v", out, err)
			}
		}
	})
}

func FuzzPersistPaths(f *testing.F) {
	for mode := range 5 {
		f.Add(byte(mode))
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		fallbackEmit := filepath.Join(t.TempDir(), "emit")
		fallbackBuckets := filepath.Join(t.TempDir(), "buckets.json")
		root := t.TempDir()
		pkgDir := filepath.Join(root, "nested", "package")
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			t.Fatal(err)
		}

		switch mode % 5 {
		case 0:
			t.Setenv(persistEnv, " false ")
			emit, buckets, persist := PersistPaths(pkgDir, fallbackEmit, fallbackBuckets)
			if persist || emit != fallbackEmit || buckets != fallbackBuckets {
				t.Fatalf("default-off result = %q, %q, %v", emit, buckets, persist)
			}
		case 1:
			t.Setenv(persistEnv, "1")
			emit, buckets, persist := PersistPaths(pkgDir, fallbackEmit, fallbackBuckets)
			if persist || emit != fallbackEmit || buckets != fallbackBuckets {
				t.Fatalf("rootless result = %q, %q, %v", emit, buckets, persist)
			}
		case 2:
			if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.25.0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv(persistEnv, " YES ")
			emit, buckets, persist := PersistPaths(pkgDir, fallbackEmit, fallbackBuckets)
			if !persist || emit != pkgDir || buckets != filepath.Join(root, "fuzz", "state", "buckets.json") {
				t.Fatalf("persistent result = %q, %q, %v", emit, buckets, persist)
			}
		case 3:
			if got, ok := findRepoRoot(""); ok || got != "" {
				t.Fatalf("empty root search = %q, %v", got, ok)
			}
		case 4:
			for _, value := range []string{"true", "on", "TRUE", "yes"} {
				if !persistTruthy(value) {
					t.Fatalf("%q is not truthy", value)
				}
			}
		}
	})
}
