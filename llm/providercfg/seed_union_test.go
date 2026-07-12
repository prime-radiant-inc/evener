package providercfg

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

func TestSeedUnionEnvExpansionEdges(t *testing.T) {
	t.Setenv("SERF_CFG_A", "alpha")
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"plain", "plain"},
		{"x$", "x$"},
		{"$$${SERF_CFG_A}", "$alpha"},
		{"$SERF_CFG_A!", "alpha!"},
		{"$-", "$-"},
	} {
		got, err := ResolveAPIKey(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("ResolveAPIKey(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{"${}", "${1BAD}", "${BAD-NAME}", "${SERF_CFG_A"} {
		if _, err := ResolveHeaderValue("X-Test", raw); err == nil {
			t.Fatalf("ResolveHeaderValue(%q) unexpectedly succeeded", raw)
		}
	}
	t.Setenv("SERF_CFG_UNSET", "")
	if _, err := ResolveAPIKey("${SERF_CFG_UNSET}"); err == nil {
		t.Fatal("empty braced environment variable unexpectedly resolved")
	}
}

func TestSeedUnionMarshalAllCompatScalars(t *testing.T) {
	truth, falsehood, three := true, false, 3
	c := &CompatConfig{
		ThinkingFormat: "chat-template", SupportsStrictMode: &truth,
		SupportsReasoningEffort: &falsehood, MaxTokensField: "max_completion_tokens",
		ToolStream: &truth, SupportsStore: &truth, SupportsDeveloperRole: &truth,
		SupportsUsageInStreaming: &truth, RequiresToolResultName: &truth,
		RequiresAssistantAfterToolResult: &truth, RequiresThinkingAsText: &truth,
		RequiresReasoningContentOnAssistant: &truth, CacheControlFormat: "anthropic",
		SupportsLongCacheRetention: &truth, SendSessionAffinityHeaders: &truth,
		LockTemperature: &truth, LockTopP: &truth, LockFrequencyPenalty: &truth,
		LockPresencePenalty: &truth, ToolChoiceAutoOnly: &truth, MaxStopSequences: &three,
		StripEmptyContent: &truth, NoJSONSchema: &truth, TranslateMaxToXHigh: &truth,
		FinishReasonMap: map[string]string{"stop": "done"},
		ChatTemplateKwargs: map[string]any{
			"bool": true, "string": "x", "int64": int64(2), "int": 3,
			"float": 1.25, "fallback": []string{"x"},
		},
	}
	cfg := Config{Default: "x", Instances: []InstanceConfig{{
		Name: "x", Type: "ollama", Compat: c,
		Models: map[string]ModelConfig{"m": {Compat: c}},
	}}}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"fallback" = "[x]"`) {
		t.Fatalf("fallback scalar missing from:\n%s", data)
	}
}

func TestSeedUnionLoadFileFailures(t *testing.T) {
	fs := afero.NewMemMapFs()
	if _, exists, err := loadFileFS(faultFS{Fs: fs, fail: "read"}, "/providers.toml"); err == nil || exists {
		t.Fatalf("failed read = exists %v, err %v", exists, err)
	}
	if err := afero.WriteFile(fs, "/invalid.toml", []byte("not = = toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := loadFileFS(fs, "/invalid.toml"); err == nil || !exists {
		t.Fatalf("invalid load = exists %v, err %v", exists, err)
	}
}

type faultFS struct {
	afero.Fs
	fail string
}

func (f faultFS) Mkdir(name string, perm os.FileMode) error {
	if f.fail == "mkdir" {
		return errors.New("injected mkdir")
	}
	return f.Fs.Mkdir(name, perm)
}
func (f faultFS) MkdirAll(path string, perm os.FileMode) error {
	if f.fail == "mkdir" {
		return errors.New("injected mkdir")
	}
	return f.Fs.MkdirAll(path, perm)
}
func (f faultFS) Open(name string) (afero.File, error) {
	if f.fail == "read" {
		return nil, errors.New("injected read")
	}
	return f.Fs.Open(name)
}
func (f faultFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if f.fail == "create" && strings.Contains(name, ".providers-") {
		return nil, errors.New("injected create")
	}
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	if strings.Contains(name, ".providers-") {
		return &faultFile{File: file, fail: f.fail}, nil
	}
	return file, nil
}
func (f faultFS) Chmod(name string, mode os.FileMode) error {
	if f.fail == "chmod" {
		return errors.New("injected chmod")
	}
	return f.Fs.Chmod(name, mode)
}
func (f faultFS) Rename(oldname, newname string) error {
	if f.fail == "rename" {
		return errors.New("injected rename")
	}
	return f.Fs.Rename(oldname, newname)
}

type faultFile struct {
	afero.File
	fail string
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.fail == "write" {
		return 0, errors.New("injected write")
	}
	return f.File.Write(p)
}
func (f *faultFile) Sync() error {
	if f.fail == "sync" {
		return errors.New("injected sync")
	}
	return f.File.Sync()
}
func (f *faultFile) Close() error {
	if f.fail == "close" {
		_ = f.File.Close()
		return errors.New("injected close")
	}
	return f.File.Close()
}
func (f *faultFile) Read(p []byte) (int, error) { return 0, io.EOF }

func TestSeedUnionWriteFileFailures(t *testing.T) {
	valid := Config{Default: "x", Instances: []InstanceConfig{{Name: "x", Type: "openai"}}}
	for _, seam := range []string{"mkdir", "create", "write", "chmod", "sync", "close", "rename"} {
		t.Run(seam, func(t *testing.T) {
			fs := faultFS{Fs: afero.NewMemMapFs(), fail: seam}
			if err := writeFileFS(fs, "/nested/providers.toml", valid); err == nil {
				t.Fatal("write unexpectedly succeeded")
			}
		})
	}
	if err := writeFileFS(afero.NewMemMapFs(), "/providers.toml", Config{}); err == nil {
		t.Fatal("invalid config write unexpectedly succeeded")
	}
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/providers.toml", []byte("not = = toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileFS(fs, "/providers.toml", valid); err != nil {
		t.Fatal(err)
	}
	if err := afero.WriteFile(fs, "/providers.toml", []byte("[instances.x]\ntype=\"openai\"\napi_key=\"saved\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileFS(fs, "/providers.toml", valid); err != nil {
		t.Fatal(err)
	}
	marshalErr := errors.New("injected codec")
	if err := writeFileCodecFS(fs, "/providers.toml", valid, func(Config) ([]byte, error) {
		return nil, marshalErr
	}); !errors.Is(err, marshalErr) {
		t.Fatalf("marshal failure = %v, want wrapped %v", err, marshalErr)
	}
}
