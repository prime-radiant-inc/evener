package identifier

import (
	"crypto/sha256"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type pipelineResolver struct {
	calls     []string
	abs       string
	eval      []string
	mainRoot  string
	mainIsGit bool
	absErr    error
	evalErr   error
	mainErr   error
}

type nilMapResolver map[string]string

func (nilMapResolver) Abs(string) (string, error)                { panic("nil map resolver called") }
func (nilMapResolver) EvalSymlinks(string) (string, error)       { panic("nil map resolver called") }
func (nilMapResolver) MainCheckout(string) (string, bool, error) { panic("nil map resolver called") }

func (r *pipelineResolver) Abs(path string) (string, error) {
	r.calls = append(r.calls, "Abs")
	if r.absErr != nil {
		return "", r.absErr
	}
	if r.abs != "" {
		return r.abs, nil
	}
	return filepath.Abs(path)
}

func (r *pipelineResolver) EvalSymlinks(path string) (string, error) {
	r.calls = append(r.calls, "EvalSymlinks")
	if r.evalErr != nil {
		return "", r.evalErr
	}
	if len(r.eval) == 0 {
		return filepath.EvalSymlinks(path)
	}
	value := r.eval[0]
	r.eval = r.eval[1:]
	return value, nil
}

func (r *pipelineResolver) MainCheckout(path string) (string, bool, error) {
	r.calls = append(r.calls, "MainCheckout")
	return r.mainRoot, r.mainIsGit, r.mainErr
}

func mustMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func independentProjectSuffix(path string) string {
	digest := sha256.Sum256([]byte(path))
	n := new(big.Int).SetBytes(digest[:])
	modulus := new(big.Int).Exp(big.NewInt(62), big.NewInt(10), nil)
	n.Mod(n, modulus)
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	out := make([]byte, 10)
	base := big.NewInt(62)
	for i := len(out) - 1; i >= 0; i-- {
		var remainder big.Int
		n.QuoRem(n, base, &remainder)
		out[i] = alphabet[remainder.Int64()]
	}
	return string(out)
}

func TestResolveProjectWithPipeline(t *testing.T) {
	root := t.TempDir()
	first := mustMkdir(t, filepath.Join(root, "first"))
	selected := mustMkdir(t, filepath.Join(root, "selected"))
	resolver := &pipelineResolver{
		abs:       filepath.Join(root, "abs", "..", "first"),
		eval:      []string{first, selected},
		mainRoot:  filepath.Join(root, "main", ".", "..", "selected"),
		mainIsGit: true,
	}
	mustMkdir(t, resolver.mainRoot)

	got, err := ResolveProjectWith("input", resolver)
	if err != nil {
		t.Fatal(err)
	}
	if got.CanonicalPath != selected {
		t.Fatalf("CanonicalPath = %q, want %q", got.CanonicalPath, selected)
	}
	if got.ID != expectedProjectIDForTest(selected) {
		t.Fatalf("ID = %q, want independently computed %q", got.ID, expectedProjectIDForTest(selected))
	}
	wantCalls := []string{"Abs", "EvalSymlinks", "MainCheckout", "EvalSymlinks"}
	if !reflect.DeepEqual(resolver.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", resolver.calls, wantCalls)
	}
}

func TestResolveProjectWithPipeline_NonGitRetainsCanonicalPath(t *testing.T) {
	root := t.TempDir()
	canonical := mustMkdir(t, filepath.Join(root, "canonical"))
	resolver := &pipelineResolver{eval: []string{canonical, canonical}}
	got, err := ResolveProjectWith("input", resolver)
	if err != nil {
		t.Fatal(err)
	}
	if got.CanonicalPath != canonical {
		t.Fatalf("CanonicalPath = %q, want %q", got.CanonicalPath, canonical)
	}
	if len(resolver.eval) != 0 {
		t.Fatalf("non-Git path did not complete final canonicalization: remaining EvalSymlinks values %v", resolver.eval)
	}
	if !reflect.DeepEqual(resolver.calls, []string{"Abs", "EvalSymlinks", "MainCheckout", "EvalSymlinks"}) {
		t.Fatalf("calls = %v", resolver.calls)
	}
}

func TestResolveProjectWithPipeline_ErrorsAndValidation(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		if _, err := ResolveProjectWith("", &pipelineResolver{}); err == nil {
			t.Fatal("empty path accepted")
		}
	})
	t.Run("nil resolver", func(t *testing.T) {
		if _, err := ResolveProjectWith(t.TempDir(), nil); err == nil {
			t.Fatal("nil resolver accepted")
		}
	})
	t.Run("typed nil resolver", func(t *testing.T) {
		var resolver *pipelineResolver
		if _, err := ResolveProjectWith(t.TempDir(), resolver); err == nil {
			t.Fatal("typed nil resolver accepted")
		}
	})
	t.Run("typed nil map resolver", func(t *testing.T) {
		var resolver nilMapResolver
		if _, err := ResolveProjectWith(t.TempDir(), resolver); !errors.Is(err, errNilResolver) {
			t.Fatalf("error = %v, want nil resolver error", err)
		}
	})
	t.Run("Abs error", func(t *testing.T) {
		want := errors.New("abs")
		_, err := ResolveProjectWith("x", &pipelineResolver{absErr: want})
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	})
	t.Run("initial EvalSymlinks error", func(t *testing.T) {
		want := errors.New("eval")
		_, err := ResolveProjectWith("x", &pipelineResolver{evalErr: want})
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	})
	t.Run("MainCheckout error", func(t *testing.T) {
		want := errors.New("git")
		_, err := ResolveProjectWith(t.TempDir(), &pipelineResolver{eval: []string{t.TempDir()}, mainErr: want})
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	})
	t.Run("Git root missing", func(t *testing.T) {
		path := t.TempDir()
		_, err := ResolveProjectWith(path, &pipelineResolver{eval: []string{path}, mainIsGit: true})
		if err == nil || !strings.Contains(err.Error(), "root") {
			t.Fatalf("error = %v, want missing Git root error", err)
		}
	})
	t.Run("selected EvalSymlinks error", func(t *testing.T) {
		want := errors.New("selected eval")
		path := t.TempDir()
		resolver := &pipelineResolver{eval: []string{path}, mainRoot: path, mainIsGit: true, evalErr: want}
		if _, err := ResolveProjectWith(path, resolver); !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	})
}

func TestProjectIDRendering(t *testing.T) {
	tests := []struct {
		name      string
		component string
		readable  string
	}{
		{"sanitized tail", "my repo!@#$-leaf", "my-repo-leaf"},
		{"collision one", "a!b", "a-b"},
		{"collision two", "a?b", "a-b"},
		{"fallback", "...", "project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := mustMkdir(t, filepath.Join(t.TempDir(), tt.component))
			if tt.name == "fallback" {
				if got := projectID("/..."); got != "project-"+independentProjectSuffix("/...") {
					t.Fatalf("projectID(\"/...\") = %q, want project fallback", got)
				}
				return
			}
			got, err := ResolveProjectWith(path, &pipelineResolver{eval: []string{path}})
			if err != nil {
				t.Fatal(err)
			}
			want := expectedProjectIDForTest(got.CanonicalPath)
			if got.ID != want {
				t.Fatalf("ID = %q, want %q", got.ID, want)
			}
			if !strings.Contains(got.ID, tt.readable) {
				t.Fatalf("ID = %q, want sanitized readable tail %q", got.ID, tt.readable)
			}
			if len(got.ID) > 80 || ValidateProjectID(got.ID) != nil {
				t.Fatalf("rendered ID %q is invalid", got.ID)
			}
		})
	}

	long := filepath.Join(t.TempDir(), strings.Repeat("x", 100), "prime-radiant-serf", "tail")
	mustMkdir(t, long)
	got, err := ResolveProjectWith(long, &pipelineResolver{eval: []string{long}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.ID, "prime-radiant-serf") {
		t.Fatalf("long ID = %q, want preserved prime-radiant-serf readable segment", got.ID)
	}
	if len(got.ID) > 80 {
		t.Fatalf("long ID length = %d, want <= 80", len(got.ID))
	}
}

func expectedProjectIDForTest(path string) string {
	parts := strings.FieldsFunc(filepath.Clean(path), func(r rune) bool { return r == '/' || r == '\\' })
	readable := make([]string, 0, len(parts))
	for _, part := range parts {
		var b strings.Builder
		lastHyphen := false
		for i := 0; i < len(part); i++ {
			c := part[i]
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
				b.WriteByte(c)
				lastHyphen = false
			} else if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
		if part = strings.Trim(b.String(), "-"); part != "" {
			readable = append(readable, part)
		}
	}
	readableText := strings.Join(readable, "-")
	if readableText == "" {
		readableText = "project"
	}
	const maxReadable = 80 - 1 - 10
	if len(readableText) > maxReadable {
		readableText = strings.TrimLeft(readableText[len(readableText)-maxReadable:], "-")
	}
	return readableText + "-" + independentProjectSuffix(path)
}

func TestValidateProjectID(t *testing.T) {
	valid := "prime-radiant-serf-0123456789"
	for _, value := range []string{valid, "a-0000000000", "project-zzzzzzzzzz"} {
		if err := ValidateProjectID(value); err != nil {
			t.Errorf("ValidateProjectID(%q) = %v", value, err)
		}
	}
	for _, value := range []string{
		"", "-0123456789", "project", "project-012345678", "project-01234567890",
		"project-012345678!", "project_0123456789", "project-012345678А", strings.Repeat("a", 81),
	} {
		if err := ValidateProjectID(value); err == nil {
			t.Errorf("ValidateProjectID(%q) accepted invalid ID", value)
		}
	}
}

var _ Resolver = (*pipelineResolver)(nil)
