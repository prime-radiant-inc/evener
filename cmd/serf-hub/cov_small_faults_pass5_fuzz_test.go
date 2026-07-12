package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/rendezvous"
)

func FuzzSmallFaultsPass5(f *testing.F) {
	for i := uint8(0); i < 12; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, variant uint8) {
		root := t.TempDir()
		cwd := filepath.Join(root, "work")
		if err := os.Mkdir(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
		mustWrite := func(name string, data []byte) {
			if err := os.WriteFile(filepath.Join(cwd, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		mustWrite("out.png", png)
		mustWrite("note.txt", []byte("hello <world>"))
		mustWrite("readme.md", []byte("# hello"))
		mustWrite("binary.bin", []byte{'x', 0, 'y'})
		mustWrite("bad.png", []byte("plain text"))

		oldHome, oldRead := configUserHomeDir, configReadFile
		oldStat, oldOpen := docStat, docOpen
		oldRecOpen, oldRecMarshal := httpRecorderOpenFile, httpRecorderMarshal
		oldMarshal, oldResolve, oldImageStat, oldImageRead := outputImageMarshal, outputImageResolve, outputImageStat, outputImageReadFile
		oldEval, oldRel, oldToken := outputImageEvalSymlinks, outputImageRel, hubTokenRead
		defer func() {
			configUserHomeDir, configReadFile = oldHome, oldRead
			docStat, docOpen = oldStat, oldOpen
			httpRecorderOpenFile, httpRecorderMarshal = oldRecOpen, oldRecMarshal
			outputImageMarshal, outputImageResolve, outputImageStat, outputImageReadFile = oldMarshal, oldResolve, oldImageStat, oldImageRead
			outputImageEvalSymlinks, outputImageRel, hubTokenRead = oldEval, oldRel, oldToken
		}()

		configUserHomeDir = func() (string, error) { return "/home/fuzz", nil }
		_ = DefaultConfig()
		_ = DefaultHubStateRoot()
		_ = DefaultConfigPath()
		_ = DefaultPastIndexDBPath()
		t.Setenv(envvars.XDGStateHome.Name, "/state")
		_ = DefaultStateGlob()
		t.Setenv(envvars.XDGStateHome.Name, "")
		_ = DefaultStateGlob()
		configUserHomeDir = func() (string, error) { return "", errors.New("home") }
		_ = DefaultHubStateRoot()
		_ = DefaultConfigPath()
		_ = DefaultStateGlob()
		_ = DefaultPastIndexDBPath()
		cfgPath := filepath.Join(root, "hub.toml")
		mustConfig := func(body string) {
			if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = LoadConfig(filepath.Join(root, "missing"))
		mustConfig("[")
		_, _ = LoadConfig(cfgPath)
		mustConfig("addr='x'\nstatus_poll_interval='1s'\npast_index_rebuild_interval='1s'\nspawn_timeout='1s'\npast_results_per_page=1\nhub_state_root='/x'\nplugin_auto_upgrade_interval='2h'\n")
		_, _ = LoadConfig(cfgPath)
		for _, d := range []string{"0s", "1s", "2m"} {
			c := Config{PluginAutoUpgradeInterval: mustDuration(d)}
			applyConfigDefaults(&c)
		}
		configReadFile = func(string) ([]byte, error) { return nil, os.ErrPermission }
		_, _ = LoadConfig(cfgPath)
		configReadFile = oldRead

		roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "01PASS5", WorkingDir: cwd}, SessionID: "01PASS5"})
		web := NewWebServer(hubcore.WebConfig{Roster: roster})
		call := func(fn func(http.ResponseWriter, *http.Request), method, target string) {
			fn(httptest.NewRecorder(), httptest.NewRequest(method, target, nil))
		}
		for _, target := range []string{
			"/doc/file?session=01PASS5&path=note.txt", "/doc/file?session=01PASS5&path=readme.md",
			"/doc/file?session=01PASS5&path=binary.bin", "/doc/file?session=01PASS5&path=missing",
			"/doc/file?session=01PASS5&path=../outside", "/doc/file?session=x&path=note.txt", "/doc/file",
		} {
			call(web.handleDocFile, http.MethodGet, target)
		}
		call(web.handleDocFile, http.MethodGet, "/doc/file?session=01PASS5&path=.")
		call(web.handleDocFile, http.MethodPost, "/doc/file")
		for _, target := range []string{"/doc/image?session=01PASS5&path=out.png", "/doc/image?session=01PASS5&path=bad.png", "/doc/image?session=01PASS5&path=missing", "/doc/image?session=01PASS5&path=../outside", "/doc/image"} {
			call(web.handleDocImage, http.MethodGet, target)
		}
		call(web.handleDocImage, http.MethodGet, "/doc/image?session=01PASS5&path=.")
		call(web.handleDocImage, http.MethodGet, "/doc/image?session=remote:x&path=out.png")
		call(web.handleDocImage, http.MethodPost, "/doc/image")
		_, _ = readDocFile(cwd)
		_, _ = readDocFile(filepath.Join(cwd, "missing"))
		for _, n := range []int{1, 1024, 1 << 20} {
			_ = formatDocBytes(n)
		}
		_ = looksBinaryBytes(append(make([]byte, 9000), 0))
		writeDocPage(httptest.NewRecorder(), "<x>", "body")
		writeDocMarkdownPage(httptest.NewRecorder(), "<x>", "# hi")
		docStat = func(string) (os.FileInfo, error) { return nil, errors.New("stat") }
		_, _ = readDocFile("x")
		docStat = oldStat
		docOpen = func(string) (*os.File, error) { return nil, errors.New("open") }
		_, _ = readDocFile(filepath.Join(cwd, "note.txt"))
		docOpen = oldOpen
		docOpen = func(string) (*os.File, error) { return os.Open(cwd) }
		_, _ = readDocFile(filepath.Join(cwd, "note.txt"))
		docOpen = oldOpen
		_, _ = web.localSessionCWD("remote:x")
		_, _ = web.localSessionCWD("01MISSING")
		past := hubcore.NewPastIndex("")
		past.SeedForTest([]schema.SessionMeta{{ID: "01PAST", EnvInfo: schema.EnvironmentInfo{WorkingDir: cwd}}, {ID: "01EMPTY"}})
		pastWeb := NewWebServer(hubcore.WebConfig{Past: past})
		_, _ = pastWeb.localSessionCWD("01PAST")
		_, _ = pastWeb.localSessionCWD("01EMPTY")

		t.Setenv(envvars.SERFRecordHTTP.Name, "")
		_ = newHTTPRequestRecorder(root)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		t.Setenv(envvars.SERFRecordHTTP.Name, "1")
		httpRecorderOpenFile = func(string, int, os.FileMode) (*os.File, error) { return nil, errors.New("open") }
		_ = newHTTPRequestRecorder(root)
		httpRecorderOpenFile = oldRecOpen
		h := newHTTPRequestRecorder(root)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { _, _ = io.ReadAll(r.Body) }))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/x?q=1", strings.NewReader(strings.Repeat("a", httpRecorderMaxBodyBytes+1))))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		httpRecorderMarshal = func(any) ([]byte, error) { return nil, errors.New("marshal") }
		newHTTPRequestRecorder(root)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		httpRecorderMarshal = oldRecMarshal

		var many strings.Builder
		for i := 0; i < 25; i++ {
			many.WriteString(fmt.Sprintf("x%d.png ", i))
		}
		_ = shellOutputImageCandidates(many.String() + ` "q one.jpg" https://x/y.png`)
		_ = shellOutputImageCandidates("https://example.com/only.png")
		for _, b := range [][]byte{nil, png, {0xff, 0xd8, 0xff}, []byte("GIF89a"), []byte("RIFF0000WEBPxxxx"), []byte("no")} {
			_, _ = supportedOutputImageMedia(b, "x")
		}
		for _, tool := range []string{"write_file", "edit_file", "apply_patch", "shell", "exec_command", "other"} {
			_ = outputImagesForToolCall("01PASS5", cwd, tool, `{"file_path":"out.png","path":"out.png"}`, "out.png out.png")
		}
		outputImageResolve = func(string, string, string, string) (appwire.OutputImage, bool) { return appwire.OutputImage{}, true }
		_ = outputImagesForToolCall("s", cwd, "shell", `{}`, "out.png")
		outputImageResolve = oldResolve
		var distinct strings.Builder
		for i := 0; i < 9; i++ {
			name := fmt.Sprintf("d%d.png", i)
			mustWrite(name, png)
			distinct.WriteString(name + " ")
		}
		_ = outputImagesForToolCall("s", cwd, "shell", `{}`, distinct.String())
		_, _ = resolveOutputImageFile("s", cwd, "../x.png", "x")
		_, _ = resolveOutputImageFile("s", cwd, "bad.png", "x")
		_, _ = resolveOutputImageFile("s", cwd, ".", "x")
		_, _, _ = readOutputImageFile(cwd)
		_, _, _ = readOutputImageFile(filepath.Join(cwd, "missing"))
		_ = outputImageSHA(png)
		_ = outputImageDisplayName("/")

		args := map[string]string{}
		item := appwire.ThreadItem{Type: "commandExecution", CallID: "c", ToolName: "write_file", ArgumentsJSON: `{"file_path":"out.png"}`, Output: "out.png"}
		params, _ := json.Marshal(map[string]any{"item": item})
		for _, n := range []appwire.Notification{{}, {Method: appwire.NotifyItemStarted}, {Method: appwire.NotifyItemStarted, Params: []byte("{")}, {Method: appwire.NotifyItemStarted, Params: []byte(`{"other":{}}`)}, {Method: appwire.NotifyItemStarted, Params: []byte(`{"item":[]}`)}, {Method: appwire.NotifyItemStarted, Params: []byte(`{"item":{"type":"other"}}`)}, {Method: appwire.NotifyItemStarted, Params: params}, {Method: appwire.NotifyItemCompleted, Params: params}} {
			_ = enrichOutputImageNotification("01PASS5", cwd, args, n)
		}
		lookupItem := appwire.ThreadItem{Type: "commandExecution", CallID: "lookup", ToolName: "write_file"}
		lookupParams, _ := json.Marshal(map[string]any{"item": lookupItem})
		args["lookup"] = `{"file_path":"out.png"}`
		_ = enrichOutputImageNotification("01PASS5", cwd, args, appwire.Notification{Method: appwire.NotifyItemCompleted, Params: lookupParams})
		noFileParams, _ := json.Marshal(map[string]any{"item": appwire.ThreadItem{Type: "commandExecution", ToolName: "shell", Output: "nothing"}})
		_ = enrichOutputImageNotification("01PASS5", cwd, args, appwire.Notification{Method: appwire.NotifyItemCompleted, Params: noFileParams})
		_ = enrichOutputImageNotification("", cwd, args, appwire.Notification{})
		outputImageMarshal = func(any) ([]byte, error) { return nil, errors.New("marshal") }
		_ = enrichOutputImageNotification("01PASS5", cwd, args, appwire.Notification{Method: appwire.NotifyItemCompleted, Params: params})
		outputImageMarshal = oldMarshal
		marshalCalls := 0
		outputImageMarshal = func(v any) ([]byte, error) {
			marshalCalls++
			if marshalCalls == 2 {
				return nil, errors.New("marshal")
			}
			return json.Marshal(v)
		}
		_ = enrichOutputImageNotification("01PASS5", cwd, args, appwire.Notification{Method: appwire.NotifyItemCompleted, Params: params})
		outputImageMarshal = oldMarshal
		outputImageStat = func(string) (os.FileInfo, error) { return nil, errors.New("stat") }
		_, _, _ = readOutputImageFile("x")
		outputImageStat = oldImageStat
		outputImageReadFile = func(string) ([]byte, error) { return nil, errors.New("read") }
		_, _, _ = readOutputImageFile(filepath.Join(cwd, "out.png"))
		outputImageReadFile = oldImageRead
		outputImageEvalSymlinks = func(string) (string, error) { return "", errors.New("eval") }
		_, _ = resolveOutputImageFile("s", cwd, "out.png", "x")
		outputImageEvalSymlinks = oldEval
		outputImageRel = func(string, string) (string, error) { return "", errors.New("rel") }
		_, _ = resolveOutputImageFile("s", cwd, "out.png", "x")
		outputImageRel = oldRel

		_, _ = newHubToken()
		hubTokenRead = func([]byte) (int, error) { return 0, errors.New("random") }
		_, _ = newHubToken()
		hubTokenRead = rand.Read
		_ = variant
	})
}

func mustDuration(s string) time.Duration { d, _ := time.ParseDuration(s); return d }
