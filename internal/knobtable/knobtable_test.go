package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// writeTree writes files (slash path -> content) under a fresh git repository,
// since the inventory asks git which files exist.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const fixtureRegistry = `package envvars

type Var struct {
	Name       string
	Visibility string
}

var (
	Read     = Var{Name: "EVENER_READ", Visibility: Public}
	ByName   = Var{Name: "EVENER_BY_NAME", Visibility: Public}
	Orphan   = Var{Name: "EVENER_ORPHAN", Visibility: Public}
	Provider = Var{Name: "ACME_API_KEY", Visibility: Public}
	Expanded = Var{Name: "ACME_ORG_ID", Visibility: Public}
	Unlisted = Var{Name: "GHOST_API_KEY", Visibility: Public}
	Isolated = Var{Name: "EVENER_ONLY_CLEARED", Visibility: Public}
	Retired  = Var{Name: "EVENER_RETIRED"}
	Internal = Var{Name: "EVENER_USED_INSIDE", Visibility: Internal}
	SetOnly  = Var{Name: "EVENER_SET_ONLY", Visibility: Public}
	Mentioned = Var{Name: "EVENER_MENTIONED", Visibility: Public}
	InMap    = Var{Name: "EVENER_IN_MAP", Visibility: Public}
	Written  = Var{Name: "EVENER_WRITTEN", Visibility: Public}
	Unset    = Var{Name: "EVENER_UNSET_ONLY", Visibility: Public}
	Unread   = Var{Name: "EVENER_UNREAD", Summary: "Unread is only named here", Visibility: Public}
)

// Unread appears in this comment and in the string below, never in code.
func Check() bool { return Internal.Name != "" && "Unread" != "" }
`

const fixtureHubConfig = `package main

import "time"

type Config struct {
	Addr        string        ` + "`toml:\"addr\"`" + `
	Timeout     time.Duration ` + "`toml:\"timeout\"`" + `
	Dead        int           ` + "`toml:\"dead\"`" + `
	StateRoot   string        ` + "`toml:\"state_root\"`" + `
	Skipped     string        ` + "`toml:\"-\"`" + `
	Hosts       []HostConfig  ` + "`toml:\"hosts\"`" + `
	Records     map[string]string ` + "`toml:\"records\"`" + `
}

const defaultTimeout = 5 * time.Second

func DefaultConfig() Config {
	root, _ := stateRoot()
	return Config{Addr: "127.0.0.1:1", Timeout: defaultTimeout, StateRoot: root}
}

var removedKnobs = []string{"old_key"}
`

func fixtureRoot(t *testing.T) string {
	return writeTree(t, map[string]string{
		"envvars/envvars.go":                    fixtureRegistry,
		"envvars/userdirs/u.go":                 "package userdirs\nvar _ = envvars.Read.Getenv()\n",
		"cmd/setter/main.go":                    "package main\n// envvars.Mentioned and \"EVENER_MENTIONED\" appear only in this comment.\nvar _ = envvars.SetOnly.Setenv(\"1\")\nfunc g() { os.Unsetenv(\"EVENER_UNSET_ONLY\") }\nfunc h() { _ = map[string]string{envvars.InMap.Name: \"1\"} }\nfunc f() { overlay[envvars.Written.Name] = \"1\"; setEnv(out, envvars.Written.Name, \"1\") }\n",
		"auth/acmetest/isolate.go":              "package acmetest\nvar _ = envvars.Isolated\n",
		"llm/registry/data/overlay.toml":        "# \"GHOST_API_KEY\" in a comment\nheaders = { \"Org\" = \"$ACME_ORG_ID\" } # \"$GHOST_API_KEY\" too\n",
		"llm/registry/note.go":                  "package registry\n// \"GHOST_API_KEY\" in a comment\nvar _ = 1\n",
		"cmd/tool/main.go":                      "package main\nvar _ = envvars.Read\nvar _ = \"EVENER_BY_NAME\"\n",
		"cmd/tool/main_test.go":                 "package main\nvar _ = envvars.Orphan\n",
		"cmd/evener-hub/config.go":              fixtureHubConfig,
		"cmd/evener-hub/use.go":                 "package main\ntype other struct{ Dead, Addr int }\nfunc f(c Config, p *Config, o other) { _ = c.Addr; _ = p.Timeout; _ = o.Dead; c.Dead = 1 }\n",
		"cmd/evener-hub/other_test.go":          "package main\nvar _ = cfg.Dead\n",
		"cmd/dev/direct.go":                     "package main\nvar _ = os.Getenv(\"SHARD_DIR\")\nvar _ = \"EVENER_READ\"\nconst rig = \"EVENER_RIG\"\nvar cleared = []string{\"EVENER_ONLY_CLEARED_LIST\"}\n",
		"docs/developing-evener/environment.md": "| `EVENER_READ` | doc |\n",
		"docs/evener-hub.md":                    "`addr` and\ntimeout = \"5s\"\n",
		"docs/superpowers/plans/dated.md":       "`EVENER_ORPHAN` `dead`\n",
	})
}

func TestEnvRowsReportConsumersAndDocRows(t *testing.T) {
	root := fixtureRoot(t)
	sources, err := scanGoSources(root)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := envRows(root, sources)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]envRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if _, ok := byName["EVENER_RETIRED"]; ok {
		t.Error("a variable with no visibility is retired and must not be listed")
	}
	if got := byName["EVENER_READ"]; !slices.Equal(got.Consumers, []string{"cmd/tool", "envvars/userdirs"}) || got.DocRow != "developing-evener/environment.md" {
		t.Errorf("EVENER_READ row = %+v, want the packages that use its identifier (a subpackage of envvars included, a literal spelling of the name excluded) and its doc row", got)
	}
	if got := byName["EVENER_ORPHAN"]; len(got.Consumers) != 0 || got.DocRow != "" {
		t.Errorf("EVENER_ORPHAN row = %+v, want no consumer (a test is not one) and no doc row (a dated plan is not one)", got)
	}
	if got := byName["ACME_ORG_ID"]; !slices.Equal(got.Consumers, []string{"llm/registry"}) {
		t.Errorf("expanded variable consumers = %v, want the registry that expands it", got.Consumers)
	}
	for _, name := range []string{"ACME_API_KEY", "GHOST_API_KEY", "EVENER_ONLY_CLEARED", "EVENER_BY_NAME", "EVENER_UNREAD", "EVENER_SET_ONLY", "EVENER_MENTIONED", "EVENER_WRITTEN", "EVENER_IN_MAP", "EVENER_UNSET_ONLY"} {
		if got := byName[name]; len(got.Consumers) != 0 {
			t.Errorf("%s consumers = %v, want none: no reader names it (a catalog entry, a function body in the registry package, or a non-test-support package that uses its envvars identifier other than to set it; a literal spelling of the name proves nothing)", name, got.Consumers)
		}
	}
	if got := byName["EVENER_USED_INSIDE"]; !slices.Equal(got.Consumers, []string{"envvars"}) {
		t.Errorf("EVENER_USED_INSIDE consumers = %v, want the registry package that reads it", got.Consumers)
	}
}

func TestDirectEnvRowsSkipRegistryNames(t *testing.T) {
	root := fixtureRoot(t)
	sources, _ := scanGoSources(root)
	rows, err := directEnvRows(root, sources)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, []string{"EVENER_RIG", "SHARD_DIR"}) {
		t.Errorf("direct rows = %v, want the names a tool declares or reads that the registry does not (a name only listed for clearing is not a knob)", names)
	}
}

func TestHubRowsResolveDefaultsAndSkipRecordTables(t *testing.T) {
	root := fixtureRoot(t)
	sources, _ := scanGoSources(root)
	rows, removed, err := hubRows(root, sources)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]hubRow{}
	var keys []string
	for _, r := range rows {
		byKey[r.Key] = r
		keys = append(keys, r.Key)
	}
	if want := []string{"addr", "timeout", "dead", "state_root"}; !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v: tables, maps and skipped fields are not scalar knobs", keys, want)
	}
	if got := byKey["timeout"].Default; got != "5 * time.Second" {
		t.Errorf("timeout default = %q, want the constant's value", got)
	}
	if got := byKey["state_root"].Default; got != "computed at startup" {
		t.Errorf("state_root default = %q, want it marked computed", got)
	}
	if got := byKey["dead"]; got.Default != "unset" || len(got.Consumers) != 0 {
		t.Errorf("dead row = %+v, want unset with no consumer (a test is not one, and neither is another struct's field of the same name)", got)
	}
	if got := byKey["addr"]; !slices.Equal(got.Consumers, []string{"cmd/evener-hub/use.go"}) || got.DocRow != "evener-hub.md" {
		t.Errorf("addr row = %+v, want its reader and its doc", got)
	}
	if got := byKey["timeout"].DocRow; got != "evener-hub.md" {
		t.Errorf("timeout doc row = %q, want a key written as an assignment to count as documented", got)
	}
	if !slices.Equal(removed, []string{"old_key"}) {
		t.Errorf("removed = %v", removed)
	}
}

// TestRepoKnobsHaveConsumers is the audit this generator exists for: a
// registered variable or hub.toml key that no code reads is dead and should be
// deleted, and a removed hub.toml key belongs in removedKnobs.
func TestRepoKnobsHaveConsumers(t *testing.T) {
	root := "../.."
	sources, err := scanGoSources(root)
	if err != nil {
		t.Fatal(err)
	}
	env, err := envRows(root, sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range env {
		if len(r.Consumers) == 0 {
			t.Errorf("%s is registered in envvars but nothing reads it", r.Name)
		}
	}
	hub, _, err := hubRows(root, sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range hub {
		if len(r.Consumers) == 0 {
			t.Errorf("hub.toml key %s is decoded but nothing reads it", r.Key)
		}
	}
}

func TestGenerateWritesOutputDoc(t *testing.T) {
	root := fixtureRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "docs", "developing-evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := generate(root); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(root, outputDoc))
	if err != nil || len(first) == 0 {
		t.Fatalf("output doc: %v (%d bytes)", err, len(first))
	}
	if err := generate(root); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(root, outputDoc))
	if !bytes.Equal(first, second) {
		t.Error("regenerating changed the output: the generator must be deterministic and ignore its own output")
	}
}

// TestIgnoredTreesDoNotChangeTheInventory: a Go file git ignores (a reference
// copy, a review cache) is not part of the repository and must not supply a
// consumer.
func TestIgnoredTreesDoNotChangeTheInventory(t *testing.T) {
	root := fixtureRoot(t)
	write := func(rel, content string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "inspo/\nllm/registry/inspo/\n")
	write("inspo/copy.go", "package inspo\nvar _ = envvars.Orphan\n")
	write("llm/registry/inspo/copy.go", "package inspo\nvar _ = \"EVENER_ORPHAN\"\n")
	write("llm/registry/inspo/broken.toml", "not = [valid\n")
	sources, err := scanGoSources(root)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := envRows(root, sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if (r.Name == "EVENER_ORPHAN" || r.Name == "GHOST_API_KEY") && len(r.Consumers) != 0 {
			t.Errorf("%s consumers = %v, want none from an ignored tree", r.Name, r.Consumers)
		}
	}
}
