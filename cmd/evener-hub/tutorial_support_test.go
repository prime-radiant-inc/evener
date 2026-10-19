package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm/registry"
)

// tutorialProviderConfig keeps provider discovery inside the owned HTTP boundary.
func tutorialProviderConfig(baseURL string) string {
	return fmt.Sprintf("default = \"fake\"\n[providers.fake]\nbase = \"openai-compatible\"\nbase_url = %q\napi_key = \"fakellm-not-a-secret\"\n[providers.ollama]\nbase_url = %q\n[providers.ollama.models.\"*\"]\ndisabled = true\n", baseURL, baseURL)
}

func writeTutorialCommandRecords(path string, records []commandRecord) error {
	b, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}

func TestTutorialProviderDiscoveryOwned(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected discovery path: %s", r.URL.Path)
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"owned-discovery-model"}]}`))
	}))
	defer server.Close()
	root := t.TempDir()
	config := filepath.Join(root, "providers.toml")
	if err := os.WriteFile(config, []byte(tutorialProviderConfig(server.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := registry.Load(registry.WithConfigPath(config), registry.WithStateRoot(root), registry.WithEnv(func(key string) (string, bool) {
		if key == "EVENER_OFFLINE" {
			return "1", true
		}
		return "", false
	}))
	if err != nil {
		t.Fatal(err)
	}
	if r.RefreshStarted() {
		t.Fatal("offline registry started catalog refresh")
	}
	// Preflight the real registry's resolved endpoint before any network call.
	// An unsafe regression fails here, never contacting the developer's Ollama.
	for _, name := range []string{"fake", "ollama"} {
		instance, ok := r.Instance(name)
		if !ok || instance.BaseURL != server.URL {
			t.Fatalf("%s discovery not owned: %+v", name, instance)
		}
	}
	client := cmdutil.NewRegistryClient(r, root)
	for _, name := range []string{"fake", "ollama"} {
		before := calls.Load()
		listing, err := client.Models(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		if !listing.Live || calls.Load() <= before {
			t.Fatalf("%s did not use owned discovery", name)
		}
	}
}

func TestTutorialChildScratchOwnership(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Never forward an ambient base, even one that looks like a valid temp dir.
	decoy := filepath.Join(t.TempDir(), "decoy")
	if err := os.Mkdir(decoy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(decoy, 0777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_HOST_TEMP_BASES", decoy)
	env := tutorialChildEnvironment(t, root)
	base := ""
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "EVENER_HOST_TEMP_BASES="); ok {
			base = value
		}
	}
	// This gate precedes the child build/run, so the red case cannot sweep host temp.
	if base != filepath.Join(root, "host-temp") {
		t.Fatalf("child host-temp base not owned: %q", base)
	}
	source := filepath.Join(t.TempDir(), "probe.go")
	const program = `package main
import ("encoding/json"; "os"; "path/filepath"; "time"; "primeradiant.com/evener/agent/sandbox")
func must(err error) { if err != nil { panic(err) } }
func stale() string {
 tmp, err := sandbox.NewSessionTmp(); must(err)
 must(tmp.Retain())
 container := filepath.Dir(tmp.Dir)
 old := time.Now().Add(-48*time.Hour); must(os.Chtimes(container,old,old))
 return container
}
func main() {
 base := os.Getenv("EVENER_HOST_TEMP_BASES")
 abandoned := stale()
 must(os.Setenv("EVENER_HOST_TEMP_BASES",os.Args[1]))
 decoy := stale()
 must(os.Setenv("EVENER_HOST_TEMP_BASES",base))
 must(sandbox.SweepCrashedSessionScratch(""))
 _, removed := os.Stat(abandoned); _, survived := os.Stat(decoy)
 tmp, err := sandbox.NewSessionTmp(); must(err); defer tmp.Remove()
 must(json.NewEncoder(os.Stdout).Encode(map[string]any{"allocated":tmp.Dir,"reclaimed":os.IsNotExist(removed),"decoy":survived==nil}))
}`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "probe")
	build := exec.Command("go", "build", "-o", binary, source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build scratch probe: %v: %s", err, output)
	}
	child := exec.Command(binary, decoy)
	child.Env = env
	output, err := child.Output()
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Allocated        string
		Reclaimed, Decoy bool
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(base, result.Allocated)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || !result.Reclaimed || !result.Decoy {
		t.Fatalf("scratch ownership failed: %+v, %v", result, err)
	}
}
