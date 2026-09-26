package hub

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/BurntSushi/toml"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// hubTOMLBanner is the machine-managed banner every hub rewrite must carry at
// the top of the file (registry spec 08 §6).
const hubTOMLBanner = "# This file is machine-managed by the evener hub.\n" +
	"# The hub rewrites it in place; comments and formatting are not preserved.\n"

// configProbe decodes a hub.toml the way the file must read back after a
// rewrite, including the key_path field the UI stores.
type configProbe struct {
	Addr              string            `toml:"addr"`
	PluginAutoUpgrade bool              `toml:"plugin_auto_upgrade"`
	Hosts             []hostConfigProbe `toml:"hosts"`
}

type hostConfigProbe struct {
	Name       string   `toml:"name"`
	SSH        string   `toml:"ssh"`
	User       string   `toml:"user"`
	EvenerPath string   `toml:"evener_path"`
	ConfigPath string   `toml:"config_path"`
	Addr       string   `toml:"addr"`
	Roots      []string `toml:"roots"`
	KeyPath    string   `toml:"key_path"`
}

// hubTOMLHostNames reads hub.toml the way the next boot does, so a test can
// assert the durable state directly.
func hubTOMLHostNames(t *testing.T, configPath string) []string {
	t.Helper()
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load hub.toml: %v", err)
	}
	names := make([]string, 0, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		names = append(names, h.Name)
	}
	return names
}

// storeHas reports whether name is in the durable host set.
func storeHas(s *hostStore, name string) bool {
	for _, e := range s.snapshot() {
		if e.Name == name {
			return true
		}
	}
	return false
}

// bootHostManager mimics production boot for these tests: hub.toml loads
// through LoadConfig, its entries build the registry, and the host manager
// gets the same selected path.
func bootHostManager(t *testing.T, configPath string) *hubHostManager {
	t.Helper()
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(%s): %v", configPath, err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	return newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, hosts, func(string, ...any) {})
}

// TestHubTOMLRewriteWritesBanner pins that add rewrites hub.toml in place and
// the rewritten file carries the machine-managed banner.
func TestHubTOMLRewriteWritesBanner(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte("addr = \"127.0.0.1:9180\"\n"), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{
		Name: "side", Address: "s.example", KeyPath: "/k/s",
	}}); err != nil {
		t.Fatalf("Add = %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	if !strings.HasPrefix(string(data), hubTOMLBanner) {
		t.Fatalf("hub.toml does not start with the machine-managed banner:\n%s", data)
	}
	var probe configProbe
	if _, err := toml.Decode(string(data), &probe); err != nil {
		t.Fatalf("hub.toml unparsable after rewrite: %v\n%s", err, data)
	}
	if len(probe.Hosts) != 1 || probe.Hosts[0].Name != "side" || probe.Hosts[0].SSH != "s.example" || probe.Hosts[0].KeyPath != "/k/s" {
		t.Fatalf("rewritten hosts = %+v, want the added entry with its key path", probe.Hosts)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat hub.toml: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("rewritten hub.toml mode = %o, want 600", info.Mode().Perm())
	}
}

// TestHubTOMLRewritePreservesEveryEntry pins that a rewrite is a
// read-modify-write of the whole file: every pre-existing host entry and
// every non-host setting survives, including the key_path field the UI
// stores.
func TestHubTOMLRewritePreservesEveryEntry(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	existing := `addr = "127.0.0.1:9199"
plugin_auto_upgrade = false

[[hosts]]
name = "alpha"
ssh = "alpha.example"
user = "u"
evener_path = "/usr/bin/evener"
config_path = "/etc/evener/hub.toml"
addr = "127.0.0.1:9180"
roots = ["/srv/a", "/srv/b"]
key_path = "/keys/alpha"

[[hosts]]
name = "beta"
ssh = "beta.example"
`
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{
		Name: "gamma", Address: "g.example",
	}}); err != nil {
		t.Fatalf("Add = %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	var probe configProbe
	if _, err := toml.Decode(string(data), &probe); err != nil {
		t.Fatalf("hub.toml unparsable after rewrite: %v\n%s", err, data)
	}
	if probe.Addr != "127.0.0.1:9199" {
		t.Fatalf("rewrite lost addr: %q", probe.Addr)
	}
	if probe.PluginAutoUpgrade {
		t.Fatalf("rewrite lost plugin_auto_upgrade = false")
	}
	byName := map[string]hostConfigProbe{}
	for _, h := range probe.Hosts {
		byName[h.Name] = h
	}
	if len(byName) != 3 {
		t.Fatalf("hosts after rewrite = %+v, want alpha, beta, gamma", probe.Hosts)
	}
	alpha := byName["alpha"]
	if alpha.SSH != "alpha.example" || alpha.User != "u" || alpha.EvenerPath != "/usr/bin/evener" ||
		alpha.ConfigPath != "/etc/evener/hub.toml" || alpha.Addr != "127.0.0.1:9180" ||
		!slices.Equal(alpha.Roots, []string{"/srv/a", "/srv/b"}) || alpha.KeyPath != "/keys/alpha" {
		t.Fatalf("alpha after rewrite = %+v, want every field preserved", alpha)
	}
	if byName["gamma"].SSH != "g.example" {
		t.Fatalf("gamma after rewrite = %+v, want the added entry", byName["gamma"])
	}
}

// TestHubTOMLWriteOrderIsTheStores pins the order contract the store's
// comments state: the boot set is seeded from the registry (name-sorted), so a
// rewrite normalizes a hand-authored out-of-order file, runtime adds append,
// and an edit keeps its entry's position. The file's host order is the hub's —
// the banner says formatting does not survive.
func TestHubTOMLWriteOrderIsTheStores(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	// Seeded deliberately out of name order: the first rewrite normalizes it.
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{
		{Name: "zeta", SSH: "zeta.example"},
		{Name: "alpha", SSH: "alpha.example"},
	}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{
		Name: "mid", Address: "mid.example",
	}}); err != nil {
		t.Fatalf("Add(mid) = %v", err)
	}
	if got := hubTOMLHostNames(t, configPath); !slices.Equal(got, []string{"alpha", "zeta", "mid"}) {
		t.Fatalf("write order after add = %v, want the name-sorted boot set then the add", got)
	}
	if _, err := m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "zeta",
		Entry: appwire.HostEntry{Address: "zeta2.example"},
	}); err != nil {
		t.Fatalf("Update(zeta) = %v", err)
	}
	if got := hubTOMLHostNames(t, configPath); !slices.Equal(got, []string{"alpha", "zeta", "mid"}) {
		t.Fatalf("write order after edit = %v, want zeta to keep its position", got)
	}
}

// TestHubTOMLMigrationMergesSidecarOnce pins the one-time migration: a
// pre-existing hub.hosts.json folds into hub.toml, the sidecar is renamed
// aside rather than deleted, and a later removal of a migrated name cannot
// resurrect through the retired file.
func TestHubTOMLMigrationMergesSidecarOnce(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte("[[hosts]]\nname = \"alpha\"\nssh = \"alpha.example\"\n"), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	sidecarPath := filepath.Join(dir, "hub.hosts.json")
	sidecarBytes := []byte(`{"hosts":[{"name":"beta","ssh":"beta.example","key_path":"/k/b"}]}`)
	if err := os.WriteFile(sidecarPath, sidecarBytes, 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	m := bootHostManager(t, configPath)
	resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "beta"})
	if err != nil {
		t.Fatalf("Status(beta) after migration = %v", err)
	}
	if resp.Host.KeyPath != "/k/b" {
		t.Fatalf("beta after migration = %+v, want its key path", resp.Host)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	var probe configProbe
	if _, err := toml.Decode(string(data), &probe); err != nil {
		t.Fatalf("hub.toml unparsable after migration: %v\n%s", err, data)
	}
	names := map[string]bool{}
	for _, h := range probe.Hosts {
		names[h.Name] = true
	}
	if !names["alpha"] || !names["beta"] || len(names) != 2 {
		t.Fatalf("hub.toml after migration = %+v, want alpha and beta exactly once", probe.Hosts)
	}
	if _, err := os.Stat(sidecarPath); !os.IsNotExist(err) {
		t.Fatalf("sidecar still present after migration: %v", err)
	}
	aside, err := os.ReadFile(sidecarPath + ".migrated")
	if err != nil || !bytes.Equal(aside, sidecarBytes) {
		t.Fatalf("sidecar set aside = %q, %v; want the original bytes preserved", aside, err)
	}
	// A later removal must not resurrect through the retired sidecar.
	sameBoot := bootHostManager(t, configPath)
	if _, err := sameBoot.Remove(context.Background(), appwire.HostRemoveParams{Name: "beta"}); err != nil {
		t.Fatalf("Remove(beta) = %v", err)
	}
	fresh := bootHostManager(t, configPath)
	if _, err := fresh.Status(context.Background(), appwire.HostStatusParams{Name: "beta"}); err == nil {
		t.Fatal("removed host resurrected from the migrated sidecar")
	}
}

// TestHubTOMLMigrationSetAsideSyncFailureIsLoudAndRetryable pins the
// migration's crash-safety story: when the set-aside rename's directory sync
// fails, the merged hub.toml is already durable, the sidecar still exists, and
// writes are refused (the caller poisons) — so nothing can act on the merged
// state while the retired file could still be re-merged. The next boot retries
// the migration and converges.
func TestHubTOMLMigrationSetAsideSyncFailureIsLoudAndRetryable(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "alpha", SSH: "alpha.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	sidecar := legacySidecarPathFor(configPath)
	sidecarBytes := []byte(`{"hosts":[{"name":"beta","ssh":"beta.example"}]}`)
	if err := os.WriteFile(sidecar, sidecarBytes, 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	boot := func(logf func(string, ...any)) *hubHostManager {
		t.Helper()
		cfg, err := LoadConfig(configPath)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		hosts, err := hostreg.New(hostRegistryEntries(cfg))
		if err != nil {
			t.Fatalf("hostreg.New: %v", err)
		}
		return newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, hosts, logf)
	}

	// Fail only the second directory sync — the set-aside rename's.
	saved := hubTOMLSyncDir
	var call int
	hubTOMLSyncDir = func(d string) error {
		call++
		if call == 2 {
			return fmt.Errorf("hub.toml directory sync: %w", syscall.EIO)
		}
		return saved(d)
	}
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	m := boot(logf)
	hubTOMLSyncDir = saved
	if len(logs) == 0 {
		t.Fatal("a failed set-aside sync produced no log line at startup")
	}
	// The merged state is durable...
	names := hubTOMLHostNames(t, configPath)
	if !slices.Equal(names, []string{"alpha", "beta"}) {
		t.Fatalf("hub.toml after the failed set-aside = %v, want alpha + beta merged", names)
	}
	// ...the retired sidecar's bytes are safe in the set-aside file (the
	// rename landed; only its durability is unproven)...
	aside, err := os.ReadFile(sidecar + legacyHostSidecarAsideSuffix)
	if err != nil || !bytes.Equal(aside, sidecarBytes) {
		t.Fatalf("sidecar set aside = %q, %v; want the original bytes", aside, err)
	}
	// ...and no mutation can land on the merged state while a crash could
	// still bring the retired file back.
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err == nil {
		t.Fatal("Add over an unmigrated sidecar succeeded, want the poisoned refusal")
	}

	// The next boot retries and converges: the sidecar is set aside with its
	// original bytes, and mutations work again.
	fresh := boot(nil)
	sideRow, err := fresh.Status(context.Background(), appwire.HostStatusParams{Name: "beta"})
	if err != nil || sideRow.Host.Address != "beta.example" {
		t.Fatalf("Status(beta) after the retry = %+v, %v", sideRow.Host, err)
	}
	if names := hubTOMLHostNames(t, configPath); !slices.Equal(names, []string{"alpha", "beta"}) {
		t.Fatalf("hub.toml after the retry = %v, want alpha + beta exactly once", names)
	}
	if _, err := fresh.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err != nil {
		t.Fatalf("Add after the retried migration = %v, want success", err)
	}
}

// TestHubTOMLRewriteLeavesNoTempFile pins that a rewrite is atomic: after the
// call no temp file remains beside the rewritten hub.toml, and the entry is
// durable.
func TestHubTOMLRewriteLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{
		Name: "side", Address: "s.example",
	}}); err != nil {
		t.Fatalf("Add = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	var probe configProbe
	if _, err := toml.Decode(string(data), &probe); err != nil {
		t.Fatalf("hub.toml unparsable after rewrite: %v\n%s", err, data)
	}
	if len(probe.Hosts) != 1 || probe.Hosts[0].Name != "side" {
		t.Fatalf("hub.toml after rewrite = %+v, want the added entry", probe.Hosts)
	}
}
