package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func baseOpts(home, cwd string) options {
	return options{
		home:       home,
		configBase: filepath.Join(home, ".config"),
		stateBase:  filepath.Join(home, ".local", "state"),
		cwd:        cwd,
	}
}

// TestExecuteMovesHomeRootFiles covers generation (a): a legacy ~/.serf home
// root holding the pre-consolidation file set moves, per file, straight into
// the final config/state roots — never through an intermediate ~/.evener.
func TestExecuteMovesHomeRootFiles(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, ".serf")
	if err := os.MkdirAll(filepath.Join(src, "run"), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "credentials.toml"), []byte("test"), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "providers.toml"), []byte("providers"), 0o644); err != nil {
		t.Fatalf("write providers: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "index.db"), []byte("db"), 0o644); err != nil {
		t.Fatalf("write index.db: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source %s should be gone", src)
	}
	configEvener := filepath.Join(home, ".config", "evener")
	stateEvener := filepath.Join(home, ".local", "state", "evener")
	if _, err := os.Stat(filepath.Join(configEvener, "credentials.toml")); err != nil {
		t.Fatalf("credentials.toml should be in the config root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configEvener, "providers.toml")); err != nil {
		t.Fatalf("providers.toml should be in the config root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateEvener, "index.db")); err != nil {
		t.Fatalf("index.db should be in the state root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateEvener, "run")); err != nil {
		t.Fatalf("run dir should be in the state root: %v", err)
	}
	if !strings.Contains(stdout.String(), "moved=4") {
		t.Fatalf("stdout = %q, want moved=4", stdout.String())
	}
}

// TestExecuteMovesInterimEvenerFiles covers generation (b): Jesse's machine
// — an interim ~/.evener (post-rename, pre-consolidation) holding the same
// file set ~/.serf used to.
func TestExecuteMovesInterimEvenerFiles(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, ".evener")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "auth-token"), []byte("tok"), 0o600); err != nil {
		t.Fatalf("write auth-token: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "hub.lock"), []byte(""), 0o644); err != nil {
		t.Fatalf("write hub.lock: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source %s should be gone", src)
	}
	stateEvener := filepath.Join(home, ".local", "state", "evener")
	if _, err := os.Stat(filepath.Join(stateEvener, "auth-token")); err != nil {
		t.Fatalf("auth-token should be in the state root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateEvener, "hub.lock")); err != nil {
		t.Fatalf("hub.lock should be in the state root: %v", err)
	}
	if !strings.Contains(stdout.String(), "moved=2") {
		t.Fatalf("stdout = %q, want moved=2", stdout.String())
	}
}

// TestExecuteHomeRootFilesPreservePermissions covers the 0600-class files:
// credentials.toml and auth-token must keep their restrictive mode across
// the move (os.Rename preserves it; MkdirAll'ing the new parent must not
// widen it).
func TestExecuteHomeRootFilesPreservePermissions(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, ".evener")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "credentials.toml"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "auth-token"), []byte("tok"), 0o600); err != nil {
		t.Fatalf("write auth-token: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr); code != 0 {
		t.Fatalf("code != 0; stderr = %q", stderr.String())
	}

	credInfo, err := os.Stat(filepath.Join(home, ".config", "evener", "credentials.toml"))
	if err != nil {
		t.Fatalf("stat migrated credentials.toml: %v", err)
	}
	if credInfo.Mode().Perm() != 0o600 {
		t.Fatalf("credentials.toml mode = %o, want 0600", credInfo.Mode().Perm())
	}
	tokInfo, err := os.Stat(filepath.Join(home, ".local", "state", "evener", "auth-token"))
	if err != nil {
		t.Fatalf("stat migrated auth-token: %v", err)
	}
	if tokInfo.Mode().Perm() != 0o600 {
		t.Fatalf("auth-token mode = %o, want 0600", tokInfo.Mode().Perm())
	}
}

// TestExecuteHomeRootFilesLegacyWinsOverInterim covers the (rare, arguably
// impossible in practice) case where both ~/.serf and ~/.evener still hold
// the same file: the legacy source is processed first and wins the move; the
// interim copy is left in place untouched (refuse-don't-clobber, not merge).
func TestExecuteHomeRootFilesLegacyWinsOverInterim(t *testing.T) {
	home := t.TempDir()
	legacySrc := filepath.Join(home, ".serf")
	interimSrc := filepath.Join(home, ".evener")
	if err := os.MkdirAll(legacySrc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(interimSrc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacySrc, "index.db"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interimSrc, "index.db"), []byte("interim"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr); code != 0 {
		t.Fatalf("code != 0; stderr = %q", stderr.String())
	}

	got, err := os.ReadFile(filepath.Join(home, ".local", "state", "evener", "index.db"))
	if err != nil {
		t.Fatalf("stat migrated index.db: %v", err)
	}
	if string(got) != "legacy" {
		t.Fatalf("migrated index.db = %q, want %q (legacy source wins)", got, "legacy")
	}
	if _, err := os.Stat(filepath.Join(interimSrc, "index.db")); err != nil {
		t.Fatalf("interim index.db should be left in place, untouched: %v", err)
	}
}

func TestExecuteIdempotent(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, ".serf")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "providers.toml"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	opts := baseOpts(home, t.TempDir())

	var stdout1, stderr1 bytes.Buffer
	if code := execute(opts, &stdout1, &stderr1); code != 0 {
		t.Fatalf("first run code = %d, want 0; stderr = %q", code, stderr1.String())
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source %s should be gone after first run", src)
	}

	var stdout2, stderr2 bytes.Buffer
	code2 := execute(opts, &stdout2, &stderr2)
	if code2 != 0 {
		t.Fatalf("second run code = %d, want 0; stderr = %q", code2, stderr2.String())
	}
	if !strings.Contains(stdout2.String(), "moved=0") {
		t.Fatalf("second run stdout = %q, want moved=0", stdout2.String())
	}
}

// TestExecuteAlreadyFinalIsNoOp covers generation (c): neither home root
// exists at all (already fully migrated, or a machine that never had one),
// and the final config/state roots already hold real content — nothing
// should move and nothing should be reported as skipped-with-a-source.
func TestExecuteAlreadyFinalIsNoOp(t *testing.T) {
	home := t.TempDir()
	configEvener := filepath.Join(home, ".config", "evener")
	stateEvener := filepath.Join(home, ".local", "state", "evener")
	if err := os.MkdirAll(configEvener, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateEvener, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configEvener, "providers.toml"), []byte("final"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "moved=0") {
		t.Fatalf("stdout = %q, want moved=0", stdout.String())
	}
	got, err := os.ReadFile(filepath.Join(configEvener, "providers.toml"))
	if err != nil || string(got) != "final" {
		t.Fatalf("final providers.toml should be untouched: content=%q err=%v", got, err)
	}
}

// TestExecuteRefusesOverwriteDestExists covers the whole-directory XDG
// config pair's refuse-don't-clobber semantics (still a directory-level move
// — its contents were already correctly split, just under the old "serf"
// name — unlike the per-file home-root migrations below).
func TestExecuteRefusesOverwriteDestExists(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, ".config", "serf")
	dst := filepath.Join(home, ".config", "evener")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "old"), []byte("old"), 0o600); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "new"), []byte("new"), 0o600); err != nil {
		t.Fatalf("write new: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(src, "old")); err != nil {
		t.Fatalf("source content should be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "new")); err != nil {
		t.Fatalf("dest content should be preserved: %v", err)
	}
	if !strings.Contains(stdout.String(), "destination already exists") {
		t.Fatalf("stdout = %q, want 'destination already exists'", stdout.String())
	}
}

// TestExecuteRefusesOverwriteHomeRootFile covers the per-file refuse-don't-
// clobber case: a homeRootFile destination that already exists (e.g. a
// previous partial migration) is left alone, and the source is preserved.
func TestExecuteRefusesOverwriteHomeRootFile(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, ".evener")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "providers.toml"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	configEvener := filepath.Join(home, ".config", "evener")
	if err := os.MkdirAll(configEvener, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configEvener, "providers.toml"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if got, err := os.ReadFile(filepath.Join(src, "providers.toml")); err != nil || string(got) != "old" {
		t.Fatalf("source content should be preserved: content=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(configEvener, "providers.toml")); err != nil || string(got) != "new" {
		t.Fatalf("dest content should be preserved: content=%q err=%v", got, err)
	}
	if !strings.Contains(stdout.String(), "destination already exists") {
		t.Fatalf("stdout = %q, want 'destination already exists'", stdout.String())
	}
}

func TestExecuteSkipsMissingSource(t *testing.T) {
	home := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "moved=0") {
		t.Fatalf("stdout = %q, want moved=0", stdout.String())
	}
	if stderr.Len() > 0 {
		t.Fatalf("stderr = %q, want empty (silent skip)", stderr.String())
	}
}

func TestExecuteDryRunMakesNoChanges(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, ".serf")
	dst := filepath.Join(home, ".config", "evener", "providers.toml")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "providers.toml"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}

	opts := baseOpts(home, t.TempDir())
	opts.dryRun = true

	var stdout, stderr bytes.Buffer
	code := execute(opts, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(src, "providers.toml")); err != nil {
		t.Fatalf("source %s should still exist: %v", src, err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dest %s should not exist", dst)
	}
	if !strings.Contains(stdout.String(), "would move") {
		t.Fatalf("stdout = %q, want 'would move'", stdout.String())
	}
	if !strings.Contains(stdout.String(), "would_move=1") {
		t.Fatalf("stdout = %q, want would_move=1", stdout.String())
	}
}

func TestExecuteRewritesLegacyPathsAfterMove(t *testing.T) {
	home := t.TempDir()
	configBase := filepath.Join(home, ".config")
	configSrc := filepath.Join(configBase, "serf")
	pluginsDir := filepath.Join(configSrc, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins dir: %v", err)
	}
	legacyMarketplaceDir := filepath.Join(configSrc, "plugins", "marketplaces", "acme")
	registry := `{"acme":{"installLocation":"` + legacyMarketplaceDir + `"}}`
	if err := os.WriteFile(filepath.Join(pluginsDir, "known_marketplaces.json"), []byte(registry), 0o600); err != nil {
		t.Fatalf("write known_marketplaces.json: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	dstFile := filepath.Join(configBase, "evener", "plugins", "known_marketplaces.json")
	got, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("reading migrated registry: %v", err)
	}
	wantMarketplaceDir := filepath.Join(configBase, "evener", "plugins", "marketplaces", "acme")
	want := `{"acme":{"installLocation":"` + wantMarketplaceDir + `"}}`
	if string(got) != want {
		t.Fatalf("migrated registry = %q, want %q", got, want)
	}
	if !strings.Contains(stdout.String(), "rewrote 1 path reference(s) in "+dstFile) {
		t.Fatalf("stdout = %q, want a rewrite logged for %s", stdout.String(), dstFile)
	}
}

func TestExecuteRerunRepairsAlreadyMigratedTree(t *testing.T) {
	home := t.TempDir()
	configBase := filepath.Join(home, ".config")
	// Simulate a machine that already ran evener-migrate: .config/evener
	// exists (so migrate() will skip the move), but the registry inside it
	// still has the stale .config/serf path from before that first run.
	pluginsDir := filepath.Join(configBase, "evener", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins dir: %v", err)
	}
	legacyMarketplaceDir := filepath.Join(configBase, "serf", "plugins", "marketplaces", "acme")
	registry := `{"acme":{"installLocation":"` + legacyMarketplaceDir + `"}}`
	regFile := filepath.Join(pluginsDir, "known_marketplaces.json")
	if err := os.WriteFile(regFile, []byte(registry), 0o600); err != nil {
		t.Fatalf("write known_marketplaces.json: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	got, err := os.ReadFile(regFile)
	if err != nil {
		t.Fatalf("reading registry: %v", err)
	}
	wantMarketplaceDir := filepath.Join(configBase, "evener", "plugins", "marketplaces", "acme")
	want := `{"acme":{"installLocation":"` + wantMarketplaceDir + `"}}`
	if string(got) != want {
		t.Fatalf("registry after re-run = %q, want %q (repaired in place)", got, want)
	}
	if !strings.Contains(stdout.String(), "rewrote 1 path reference(s) in "+regFile) {
		t.Fatalf("stdout = %q, want a rewrite logged for %s", stdout.String(), regFile)
	}

	// Re-running again must be idempotent: no further rewrites reported.
	var stdout2, stderr2 bytes.Buffer
	code2 := execute(baseOpts(home, t.TempDir()), &stdout2, &stderr2)
	if code2 != 0 {
		t.Fatalf("second re-run code = %d, want 0; stderr = %q", code2, stderr2.String())
	}
	if strings.Contains(stdout2.String(), "rewrote") {
		t.Fatalf("second re-run stdout = %q, want no further rewrites", stdout2.String())
	}
}

func TestExecuteDryRunDoesNotRewriteContent(t *testing.T) {
	home := t.TempDir()
	configBase := filepath.Join(home, ".config")
	configSrc := filepath.Join(configBase, "serf", "plugins")
	if err := os.MkdirAll(configSrc, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacyMarketplaceDir := filepath.Join(configBase, "serf", "plugins", "marketplaces", "acme")
	registry := `{"acme":{"installLocation":"` + legacyMarketplaceDir + `"}}`
	regFile := filepath.Join(configSrc, "known_marketplaces.json")
	if err := os.WriteFile(regFile, []byte(registry), 0o600); err != nil {
		t.Fatalf("write known_marketplaces.json: %v", err)
	}

	opts := baseOpts(home, t.TempDir())
	opts.dryRun = true
	var stdout, stderr bytes.Buffer
	code := execute(opts, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	got, err := os.ReadFile(regFile)
	if err != nil {
		t.Fatalf("reading registry: %v", err)
	}
	if string(got) != registry {
		t.Fatalf("dry-run modified content: got %q, want unchanged %q", got, registry)
	}
}

// TestExecuteRewritesCrossFileHomeRootReferences covers a hand-edited
// hub.toml (README-documented: hub_state_root/run_dir/past_index_db can
// point at ~/.evener paths): after hub.toml itself moves to the config root
// and index.db moves to the state root, hub.toml's OWN embedded reference to
// the old index.db path — a sibling file, not hub.toml's own old path — must
// be rewritten to the new one too.
func TestExecuteRewritesCrossFileHomeRootReferences(t *testing.T) {
	home := t.TempDir()
	interim := filepath.Join(home, ".evener")
	if err := os.MkdirAll(interim, 0o755); err != nil {
		t.Fatal(err)
	}
	hubToml := "past_index_db = \"" + filepath.Join(interim, "index.db") + "\"\n"
	if err := os.WriteFile(filepath.Join(interim, "hub.toml"), []byte(hubToml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interim, "index.db"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr); code != 0 {
		t.Fatalf("code != 0; stderr = %q", stderr.String())
	}

	newHubToml := filepath.Join(home, ".config", "evener", "hub.toml")
	newIndexDB := filepath.Join(home, ".local", "state", "evener", "index.db")
	got, err := os.ReadFile(newHubToml)
	if err != nil {
		t.Fatalf("reading migrated hub.toml: %v", err)
	}
	want := "past_index_db = \"" + newIndexDB + "\"\n"
	if string(got) != want {
		t.Fatalf("migrated hub.toml = %q, want %q", got, want)
	}
}

func TestExecuteMigratesXDGConfigAndState(t *testing.T) {
	home := t.TempDir()
	configBase := filepath.Join(home, ".config")
	stateBase := filepath.Join(home, ".local", "state")

	configSrc := filepath.Join(configBase, "serf")
	if err := os.MkdirAll(filepath.Join(configSrc, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir config source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configSrc, "mcp.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write mcp.json: %v", err)
	}
	stateSrc := filepath.Join(stateBase, "serf")
	if err := os.MkdirAll(filepath.Join(stateSrc, "projects"), 0o755); err != nil {
		t.Fatalf("mkdir state source: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(configSrc); !os.IsNotExist(err) {
		t.Fatalf("config source %s should be gone", configSrc)
	}
	if _, err := os.Stat(filepath.Join(configBase, "evener", "skills")); err != nil {
		t.Fatalf("config skills not in dest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configBase, "evener", "mcp.json")); err != nil {
		t.Fatalf("mcp.json not in config dest: %v", err)
	}

	if _, err := os.Stat(stateSrc); !os.IsNotExist(err) {
		t.Fatalf("state source %s should be gone", stateSrc)
	}
	if _, err := os.Stat(filepath.Join(stateBase, "evener", "projects")); err != nil {
		t.Fatalf("state projects not in dest: %v", err)
	}
}

func TestExecuteMigratesProjectSerfDirectory(t *testing.T) {
	home := t.TempDir()
	projectDir := t.TempDir()
	src := filepath.Join(projectDir, ".serf")
	dst := filepath.Join(projectDir, ".evener")
	if err := os.MkdirAll(filepath.Join(src, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir project source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "mcp.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write project mcp.json: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, projectDir), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("project source %s should be gone", src)
	}
	if _, err := os.Stat(filepath.Join(dst, "mcp.json")); err != nil {
		t.Fatalf("project mcp.json not in dest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "prompts")); err != nil {
		t.Fatalf("project prompts not in dest: %v", err)
	}
}

// TestExecuteHandlesPartialMigration covers a machine mid-migration: one
// home-root file already landed at its final destination (e.g. an earlier,
// interrupted run) while the interim ~/.evener still holds a different,
// unmigrated file. A single run must finish the rest without disturbing what
// already landed.
func TestExecuteHandlesPartialMigration(t *testing.T) {
	home := t.TempDir()

	stateEvener := filepath.Join(home, ".local", "state", "evener")
	if err := os.MkdirAll(stateEvener, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateEvener, "index.db"), []byte("final"), 0o644); err != nil {
		t.Fatal(err)
	}

	interim := filepath.Join(home, ".evener")
	if err := os.MkdirAll(interim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interim, "providers.toml"), []byte("cfg"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(home, ".config", "evener", "providers.toml")); err != nil {
		t.Fatalf("providers.toml should now be migrated: %v", err)
	}
	// providers.toml (interim -> config root) is the only real move; index.db
	// is already at its final home (skip, dest exists).
	if !strings.Contains(stdout.String(), "moved=1") {
		t.Fatalf("stdout = %q, want moved=1", stdout.String())
	}
	got, err := os.ReadFile(filepath.Join(stateEvener, "index.db"))
	if err != nil || string(got) != "final" {
		t.Fatalf("already-final index.db should be untouched: content=%q err=%v", got, err)
	}
	// The interim root held only providers.toml, now moved out: it should be gone.
	if _, err := os.Stat(interim); !os.IsNotExist(err) {
		t.Fatalf("emptied interim root %s should have been removed", interim)
	}
}

// TestExecuteLeavesNonEmptyHomeRootInPlace covers a home root that still
// holds content evener-migrate does not recognize (not one of
// homeRootFiles): the known file moves, but the directory itself — and the
// unrecognized file — are left alone rather than force-deleted.
func TestExecuteLeavesNonEmptyHomeRootInPlace(t *testing.T) {
	home := t.TempDir()
	interim := filepath.Join(home, ".evener")
	if err := os.MkdirAll(interim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interim, "providers.toml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interim, "unknown-file.txt"), []byte("mystery"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr); code != 0 {
		t.Fatalf("code != 0; stderr = %q", stderr.String())
	}

	if _, err := os.Stat(filepath.Join(home, ".config", "evener", "providers.toml")); err != nil {
		t.Fatalf("providers.toml should be migrated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(interim, "unknown-file.txt")); err != nil {
		t.Fatalf("unrecognized content should be left in place: %v", err)
	}
}

func TestExecuteVerbosePrintsSkippedSources(t *testing.T) {
	home := t.TempDir()
	opts := baseOpts(home, t.TempDir())
	opts.verbose = true

	var stdout, stderr bytes.Buffer
	code := execute(opts, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "source does not exist") {
		t.Fatalf("stdout = %q, want 'source does not exist' with --verbose", stdout.String())
	}
}

func TestRunRespectsXDGEnvVars(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "custom-config")
	stateHome := filepath.Join(home, "custom-state")

	configSrc := filepath.Join(configHome, "serf")
	if err := os.MkdirAll(filepath.Join(configSrc, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir config source: %v", err)
	}
	stateSrc := filepath.Join(stateHome, "serf")
	if err := os.MkdirAll(filepath.Join(stateSrc, "projects"), 0o755); err != nil {
		t.Fatalf("mkdir state source: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	// Isolate cwd to a temp dir to prevent the project scan from finding
	// unrelated .evener directories in ancestor git roots.
	cwd := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldwd)

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(configSrc); !os.IsNotExist(err) {
		t.Fatalf("config source %s should be gone", configSrc)
	}
	if _, err := os.Stat(filepath.Join(configHome, "evener", "skills")); err != nil {
		t.Fatalf("config skills not in dest: %v", err)
	}
	if _, err := os.Stat(stateSrc); !os.IsNotExist(err) {
		t.Fatalf("state source %s should be gone", stateSrc)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "evener", "projects")); err != nil {
		t.Fatalf("state projects not in dest: %v", err)
	}
}

func TestRunRejectsPositionalArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"unexpected"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, want 2; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "positional arguments are not accepted") {
		t.Fatalf("stderr = %q, want positional arguments rejection", stderr.String())
	}
}
