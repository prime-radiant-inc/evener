package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// This journey starts with an empty providers.toml and no credentials. Only
// the external HTTP provider is scripted, never the UI, hub, daemon or tools.
func TestTutorialFirstProviderWeb(t *testing.T) {
	if os.Getenv("EVENER_TUTORIAL_TOURS") != "1" {
		t.Skip("opt-in tutorial browser journey")
	}
	if testing.Short() {
		t.Fatal("explicit tutorial browser journey cannot run with -short")
	}
	p, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	credential := hex.EncodeToString(key[:])
	target, err := url.Parse("http://" + p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	var models, generations, rejectedDiscovery atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Even keyless Ollama discovery goes to this owned server, not an ambient
		// localhost service. It is unavailable, not another configured provider.
		if strings.HasPrefix(r.URL.Path, "/unavailable/") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// Creating an optional-bearer instance can trigger discovery before the
		// key form is submitted. Refuse it, without treating it as authenticated
		// evidence. Generation and every nonempty credential must still match.
		if r.URL.Path == "/v1/models" && r.Method == http.MethodGet && r.Header.Get("Authorization") == "" {
			rejectedDiscovery.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+credential {
			t.Error("provider did not receive the credential saved by the browser")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			if r.Method != http.MethodGet || r.ContentLength > 0 {
				t.Error("discovery was not a bodyless GET")
			}
			models.Add(1)
		case "/v1/chat/completions":
			if r.Method != http.MethodPost {
				t.Error("generation was not a POST")
			}
			generations.Add(1)
		default:
			t.Error("unexpected provider route")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(endpoint.Close)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := settingsUIControllerBinary(t, repo)
	env := append(tutorialChildEnvironment(t, tutorialOwnedRoot(t)), "OLLAMA_BASE_URL="+endpoint.URL+"/unavailable")
	stack := startHubStackWithOptions(t, "", "fake/"+fakellm.ModelID, bin, hubStackOptions{ChildEnv: env})
	stack.workDir, err = filepath.EvalSymlinks(stack.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if r := recordCommand(t, stack.workDir, tutorialChildEnvironment(t, stack.home), "git", "init"); r.ExitCode != 0 {
		t.Fatal("workspace git init")
	}
	configPath := filepath.Join(stack.home, "config", "evener", "providers.toml")
	credentialPath := filepath.Join(stack.home, "config", "evener", "credentials.toml")
	config, err := os.ReadFile(configPath)
	if err != nil || len(config) != 0 {
		t.Fatal("provider configuration was not empty")
	}
	if _, err := os.Stat(credentialPath); !os.IsNotExist(err) {
		t.Fatal("credential store already exists")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	observer := stack.dialRPC(ctx, t)
	listModels := func() appwire.ModelListResponse {
		result, err := clientRequest[appwire.ModelListResponse](ctx, observer, appwire.MethodModelList, appwire.ModelListParams{Refresh: true})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if len(listModels().Data) != 0 {
		t.Fatal("empty configuration already offered launchable models")
	}
	assertHubServesBuiltSPA(t, stack.addr, stack.token)
	out := t.TempDir()
	hubURL := "http://" + stack.addr
	driver := openTutorialDriver(ctx, t, map[string]any{"url": hubURL, "authURL": hubURL + "/auth/" + stack.token, "workspace": stack.workDir, "model": stack.model, "scenario": "first-provider", "viewport": map[string]int{"width": 1280, "height": 900}, "outputDir": out})
	driver.act(t, "setup-provider", map[string]any{"baseURL": endpoint.URL + "/v1", "credential": credential, "providerID": "fake", "modelID": fakellm.ModelID})
	// Read persisted files independently of the browser's success UI and cache.
	config, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Providers map[string]struct {
			Base    string `toml:"base"`
			BaseURL string `toml:"base_url"`
		} `toml:"providers"`
	}
	if _, err := toml.Decode(string(config), &saved); err != nil {
		t.Fatal("saved provider TOML is invalid")
	}
	if len(saved.Providers) != 1 || saved.Providers["fake"].Base != "openai-compatible" || saved.Providers["fake"].BaseURL != endpoint.URL+"/v1" {
		t.Fatal("UI did not persist the intended provider endpoint")
	}
	if bytes.Contains(config, []byte(credential)) {
		t.Fatal("credential leaked into provider config")
	}
	store, err := credentials.LoadStore(credentialPath)
	if err != nil {
		t.Fatal("cannot read private saved credentials")
	}
	if got, ok := store.Get("fake"); !ok || got != credential || len(store.Names()) != 1 {
		t.Fatal("saved credential differs")
	}
	info, err := os.Stat(credentialPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credentials are not owner-only")
	}
	if models.Load() == 0 || generations.Load() != 0 {
		t.Fatal("setup must discover models without generating")
	}
	threads, err := clientRequest[appwire.ThreadListResponse](ctx, observer, appwire.MethodThreadList, appwire.ThreadListParams{})
	if err != nil || len(threads.Data) != 0 {
		t.Fatal("setup created a session before Start")
	}
	before := models.Load()
	listing := listModels()
	found := false
	for _, m := range listing.Data {
		if m.Provider == "fake" && m.Model == fakellm.ModelID {
			found = true
		}
	}
	if !found || models.Load() <= before {
		t.Fatal("fresh model discovery did not reach the saved provider")
	}
	const prompt = "PROVIDER_TASK_63ac"
	const answer = "PROVIDER_DONE_63ac"
	driver.act(t, "start", map[string]any{"prompt": prompt})
	call := dailyNext(ctx, t, p)
	thread := configRunningThread(ctx, t, observer, stack.workDir, prompt, call)
	call = dailyWrite(ctx, t, p, call, "provider-result.json", "63ac")
	call.RespondToolCall("communicate", communicateArgs(answer))
	awaitThread(ctx, t, observer, thread.Evener.Ref, "provider task completed", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == "" && thread.Status.Type != "active"
	})
	if err := dailyEffectOracle(filepath.Join(stack.workDir, "provider-result.json"), "63ac"); err != nil {
		t.Fatal(err)
	}
	meta, path := configMeta(t, filepath.Join(stack.home, "state"), thread.SessionID)
	if meta.OriginalPrompt != prompt {
		t.Fatal("saved session lost task input")
	}
	body, err := os.ReadFile(strings.TrimSuffix(path, ".meta.json") + ".transcript.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var input, reply, completed bool
	for line := range bytes.SplitSeq(bytes.TrimSpace(body), []byte("\n")) {
		var entry transcript.Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Turn.Kind == schema.TurnUserInput {
			input = input || entry.Turn.Message.Text() == prompt
		}
		if c := entry.Turn.Communicate; c != nil {
			reply = reply || (c.EndTurn && c.Message == answer)
		}
		if c := entry.Turn.Completion; c != nil {
			completed = completed || c.Status == schema.TurnCompleted
		}
	}
	if !input || !reply || !completed {
		t.Fatal("real session lacks durable input, reply or completion")
	}
	if bytes.Contains(body, []byte(credential)) {
		t.Fatal("credential leaked into transcript")
	}
	driver.act(t, "close", nil)
	files, err := os.ReadDir(out)
	if err != nil || len(files) != 0 {
		t.Fatal("provider setup produced capture artifacts")
	}
	t.Logf("executed empty-config browser setup: %d authenticated model-list requests, %d rejected pre-key discovery requests, %d generation requests, private saved credential, completed real session and independent file effect", models.Load(), rejectedDiscovery.Load(), generations.Load())
}
