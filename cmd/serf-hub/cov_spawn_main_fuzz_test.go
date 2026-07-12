package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/launchconfig"
	"primeradiant.com/serf/envvars"
)

func FuzzSpawnMainHelpers(f *testing.F) {
	for _, seed := range []struct {
		op   byte
		data string
	}{
		{0, ""}, {0, "addr = 'localhost:1'\nplugin_auto_upgrade_interval = '1s'\n"},
		{0, "["}, {1, "xdg"}, {1, "windows-profile"}, {1, "windows-drive"},
		{1, "windows-temp"}, {1, "home"}, {1, "temp"}, {2, "body"},
		{2, strings.Repeat("x", httpRecorderMaxBodyBytes+1)}, {3, "dev"}, {3, "embed"},
		{4, "args"}, {4, "empty"}, {5, "tail"}, {5, "token"},
	} {
		f.Add(seed.op, seed.data)
	}

	f.Fuzz(func(t *testing.T, op byte, data string) {
		switch op % 6 {
		case 0:
			root := t.TempDir()
			path := filepath.Join(root, "hub.toml")
			if data != "" {
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := LoadConfig(path)
			if data == "[" && err == nil {
				t.Fatal("invalid TOML accepted")
			}
			if err == nil && cfg.Addr == "" {
				t.Fatal("default address missing")
			}
			cfg = Config{PluginAutoUpgradeInterval: -time.Second}
			applyConfigDefaults(&cfg)
			if cfg.PluginAutoUpgradeInterval != 12*time.Hour {
				t.Fatal("negative interval not defaulted")
			}
			cfg.PluginAutoUpgradeInterval = time.Second
			applyConfigDefaults(&cfg)
			if cfg.PluginAutoUpgradeInterval != time.Minute {
				t.Fatal("tiny interval not floored")
			}
		case 1:
			env := map[string]string{}
			goos := "linux"
			switch data {
			case "xdg":
				env["XDG_STATE_HOME"] = " /state "
			case "windows-profile":
				goos, env["USERPROFILE"] = "windows", " C:\\Users\\u "
			case "windows-drive":
				goos, env["HOMEDRIVE"], env["HOMEPATH"] = "windows", "C:", "\\Users\\u"
			case "windows-temp":
				goos = "windows"
			case "home":
				env["HOME"] = " /home/u "
			}
			got := openAIStateDirFromLookup(goos, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
			if got == "" {
				t.Fatal("empty state dir")
			}
			_ = openAIStateDirFromEnvMap(env)
			_ = openAIStateDirFromEnvList([]string{"HOME=/home/u", "HOME=/last"})
		case 2:
			root := t.TempDir()
			t.Setenv(envvars.SERFRecordHTTP.Name, "1")
			var downstream string
			h := newHTTPRequestRecorder(root)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				downstream = string(b)
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodPost, "/p?q=1", strings.NewReader(data))
			req.Header.Set("X-Test", "yes")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if downstream != data || rr.Code != http.StatusNoContent {
				t.Fatal("recorder changed request or response")
			}
			line, err := os.ReadFile(filepath.Join(root, "hub-http.jsonl"))
			if err != nil || len(line) == 0 {
				t.Fatalf("recording missing: %v", err)
			}
			t.Setenv(envvars.SERFRecordHTTP.Name, "")
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
			if got := newHTTPRequestRecorder(root)(next); got == nil {
				t.Fatal("identity middleware returned nil")
			}
		case 3:
			root := t.TempDir()
			if data == "dev" {
				if err := os.MkdirAll(filepath.Join(root, "assets"), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("SERF_HUB_ASSETS_DIR", root)
			} else {
				t.Setenv("SERF_HUB_ASSETS_DIR", "")
			}
			if templatesRoot() == nil || assetsRoot() == nil || devAssetsDir() != os.Getenv("SERF_HUB_ASSETS_DIR") {
				t.Fatal("asset roots unavailable")
			}
			rr := httptest.NewRecorder()
			noStore(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(201) })).ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
			if rr.Code != 201 || rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("no-store wrapper failed")
			}
			if !strings.HasPrefix(assetVersionQuery(), "?v=") {
				t.Fatal("bad asset version")
			}
		case 4:
			resolved := launchconfig.Resolved{}
			if data == "args" {
				resolved.Effective.Model = "model"
				n := 3
				resolved.Effective.AppReplaySize = &n
			}
			s := hubcore.SpawnRequest{WorkingDir: "/work", StateDir: "/state", RunDir: "/run", AppReplaySize: 2, Resolved: resolved}
			r := hubcore.ResumeRequest{SessionID: "sid", WorkingDir: "/work", StateDir: "/state", RunDir: "/run", AppReplaySize: 2, Resolved: resolved}
			if len(buildSpawnArgs(s)) < 2 || len(buildResumeArgs(r)) < 5 {
				t.Fatal("short launch args")
			}
			if got := resolveSerfStateDirWithStateHome("/a/b", "/override", "/xdg"); got != "/override" {
				t.Fatal(got)
			}
			if got := resolveSerfStateDirWithStateHome("/a/b", "", "/xdg"); !strings.Contains(got, "serf") {
				t.Fatal(got)
			}
		case 5:
			if data == "token" {
				tok, err := newHubToken()
				b, decErr := base64.RawURLEncoding.DecodeString(tok)
				if err != nil || decErr != nil || len(b) != 32 {
					t.Fatalf("bad token: %v %v", err, decErr)
				}
				return
			}
			var b tailBuffer
			b.limit = 4
			_, _ = b.Write([]byte("abcdef"))
			if b.String() != "cdef" {
				t.Fatalf("tail=%q", b.String())
			}
			_, _ = b.Write([]byte("gh"))
			if b.String() != "efgh" {
				t.Fatalf("tail=%q", b.String())
			}
			if launchFailureError("launch", nil, "") == nil {
				t.Fatal("missing launch error")
			}
		}
	})
}
