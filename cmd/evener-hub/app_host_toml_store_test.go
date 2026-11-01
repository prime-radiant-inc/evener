package hub

import (
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
	cfg, err := loadWithRecords(configPath)
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

// loadedConfig is hub.toml decoded with the reserved record sections it still
// carries, the shape a manager keeps its records in when no operation store is
// configured.
type loadedConfig struct {
	Config
	hostRecordSet
}

func loadWithRecords(path string) (loadedConfig, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		return loadedConfig{}, err
	}
	records, err := readLegacyRecords(path)
	if err != nil {
		return loadedConfig{}, err
	}
	return loadedConfig{Config: cfg, hostRecordSet: records}, nil
}

// hostRegistryEntries maps the file's [[hosts]] onto registry values with the
// identities its record sections persist.
func hostRegistryEntries(cfg loadedConfig) []hostreg.Host {
	return hostEntriesWithIdentity(cfg.Hosts, cfg.hostRecordSet)
}

// bootHostManager mimics production boot for these tests: hub.toml loads
// through LoadConfig, its entries build the registry, and the host manager
// gets the same selected path.
func bootHostManager(t *testing.T, configPath string) *hubHostManager {
	t.Helper()
	cfg, err := loadWithRecords(configPath)
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
plugin_auto_upgrade = true

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
	if !probe.PluginAutoUpgrade {
		t.Fatalf("rewrite lost plugin_auto_upgrade = true")
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
	if _, err := m.Update(context.Background(), updateRequest(t, m, "zeta", appwire.HostEntry{Address: "zeta2.example"})); err != nil {
		t.Fatalf("Update(zeta) = %v", err)
	}
	if got := hubTOMLHostNames(t, configPath); !slices.Equal(got, []string{"alpha", "zeta", "mid"}) {
		t.Fatalf("write order after edit = %v, want zeta to keep its position", got)
	}
}

// TestHubTOMLRollbackPreservesAHandAddedEntry pins the compensation path the
// reviewer named: a post-rename write failure rolls the live set back WITHOUT
// dropping an operator's hand-added file entry (the rollback's known set is
// the union of the mutation's before and after sets, so only the mutation's
// own change is undone).
func TestHubTOMLRollbackPreservesAHandAddedEntry(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "alpha", SSH: "alpha.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	raw = append(raw, []byte("\n[[hosts]]\nname = \"hand\"\nssh = \"hand.example\"\n")...)
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatalf("hand-edit hub.toml: %v", err)
	}
	// The add's staged write fails behind its own rename, so the call
	// compensates by rewriting the prior live set.
	saved := hubTOMLSyncDir
	var call int
	hubTOMLSyncDir = func(d string) error {
		call++
		if call == 1 {
			return fmt.Errorf("hub.toml directory sync: %w", syscall.EIO)
		}
		return saved(d)
	}
	// The swap must not outlive this test even when a Fatalf fires before the
	// explicit restore below.
	t.Cleanup(func() { hubTOMLSyncDir = saved })
	_, err = m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "staged", Address: "staged.example"}})
	hubTOMLSyncDir = saved
	if err == nil {
		t.Fatal("Add over a failing directory sync succeeded, want the refusal")
	}
	if got := hubTOMLHostNames(t, configPath); !slices.Equal(got, []string{"alpha", "hand"}) {
		t.Fatalf("hub.toml after the compensated add = %v, want alpha + the hand-added entry (never the staged one)", got)
	}
}

// TestHubTOMLRewritePreservesAHandAddedEntry pins the rewrite's treatment of
// out-of-band edits: a host the operator added to hub.toml while the hub was
// running is not live (the boot set is what the hub serves), but a rewrite must
// carry it through rather than silently delete it — it becomes live on the
// next boot.
func TestHubTOMLRewritePreservesAHandAddedEntry(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "alpha", SSH: "alpha.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	if _, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "alpha"}); err != nil {
		t.Fatalf("Status(alpha) = %v", err)
	}
	// The operator hand-adds a host while the hub runs.
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	raw = append(raw, []byte("\n[[hosts]]\nname = \"hand\"\nssh = \"hand.example\"\n")...)
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatalf("hand-edit hub.toml: %v", err)
	}
	// A UI mutation rewrites the file; the hand-added entry survives.
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "new", Address: "new.example"}}); err != nil {
		t.Fatalf("Add(new) = %v", err)
	}
	if got := hubTOMLHostNames(t, configPath); !slices.Equal(got, []string{"alpha", "new", "hand"}) {
		t.Fatalf("hub.toml after the rewrite = %v, want the hand-added entry carried through", got)
	}
	// It is not live yet (the boot set is the running hub's host set)...
	if _, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "hand"}); err == nil {
		t.Fatal("a hand-added entry served without a boot")
	}
	// ...and a fresh boot makes it live.
	fresh := bootHostManager(t, configPath)
	if _, err := fresh.Status(context.Background(), appwire.HostStatusParams{Name: "hand"}); err != nil {
		t.Fatalf("Status(hand) after the next boot = %v", err)
	}
}

// TestHubTOMLRewritePreservesAHandAddedEntryWithAnEmptyStore pins the
// empty-store edge of the preservation rule: a hub whose hub.toml declared zero
// hosts at boot hands the writer an empty pre-save snapshot. nil is the
// writer's exact-write sentinel ("drop nothing back"), so an empty store must
// still hand back a real, non-nil snapshot — otherwise the first UI mutation on
// such a hub silently drops the operator's hand-added entry.
func TestHubTOMLRewritePreservesAHandAddedEntryWithAnEmptyStore(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	// A host-less hub.toml: the banner only — no hosts key at all, so the
	// operator's appended [[hosts]] table is a legal edit (a file the hub
	// wrote with zero hosts carries `hosts = []`, which cannot be reopened as
	// an array of tables; that case refuses loudly instead of dropping).
	if err := os.WriteFile(configPath, []byte(hostTOMLBanner), 0o600); err != nil {
		t.Fatalf("seed host-less hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	// The operator hand-adds a host while the hub runs.
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	raw = append(raw, []byte("\n[[hosts]]\nname = \"hand\"\nssh = \"hand.example\"\n")...)
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatalf("hand-edit hub.toml: %v", err)
	}
	// The first UI mutation's rewrite must carry it through.
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "new", Address: "new.example"}}); err != nil {
		t.Fatalf("Add(new) = %v", err)
	}
	if got := hubTOMLHostNames(t, configPath); !slices.Equal(got, []string{"new", "hand"}) {
		t.Fatalf("hub.toml after the rewrite = %v, want the hand-added entry carried through", got)
	}
}

// TestHubTOMLRewriteWithNoHostsOmitsTheHostsKey pins the empty-set shape: a
// rewrite with zero entries leaves no `hosts` key at all, so a later operator
// hand-add can append a `[[hosts]]` table. `hosts = []` cannot be reopened as
// an array of tables, which would make the next boot refuse the file.
func TestHubTOMLRewriteWithNoHostsOmitsTheHostsKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.toml")
	if err := writeHubTOMLHosts(path, nil); err != nil {
		t.Fatalf("write empty hub.toml: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	if _, err := decodeConfig(path, string(data)); err != nil {
		t.Fatalf("empty rewrite is not loadable: %v", err)
	}
	handEdited := string(data) + "\n[[hosts]]\nname = \"hand\"\nssh = \"hand.example\"\n"
	if _, err := decodeConfig(path, handEdited); err != nil {
		t.Fatalf("a hand-added [[hosts]] over an empty rewrite does not load: %v", err)
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

// TestHubTOMLLegacyBootstrapRecordLoadsAndIsDroppedOnRewrite pins the upgrade
// path for the retired crash-fencing bootstrap keys. The removed first-contact
// caller persisted the attempt fence into hub.toml before it refused (and a
// converged attempt also wrote helper_installed with helper_version), while the
// reserved-record rule refuses a reserved field this build does not decode —
// so dropping those keys from the record struct outright would make every file
// the prior build wrote unloadable at boot. They must decode, load, and be
// dropped by the next rewrite.
func TestHubTOMLLegacyBootstrapRecordLoadsAndIsDroppedOnRewrite(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	legacy := `[[hosts]]
name = "alpha"
ssh = "alpha.example"

[host_records.alpha]
incarnation_id = "inc-alpha"
presence_epoch = 7
bootstrap_attempted = true
bootstrap_epoch_boot = "boot-1"
bootstrap_epoch_op_seq = 3
bootstrap_attempt_token = "test"
helper_installed = true
helper_version = 1
`
	if err := os.WriteFile(configPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	if _, err := loadWithRecords(configPath); err != nil {
		t.Fatalf("a hub.toml from the bootstrap build refused to load: %v", err)
	}
	m := bootHostManager(t, configPath)
	// An unrelated mutation rewrites the file: the retired keys must not ride
	// through the record preservation rule into the new file.
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{
		Name: "side", Address: "s.example",
	}}); err != nil {
		t.Fatalf("Add = %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	var probe struct {
		HostRecords map[string]map[string]any `toml:"host_records"`
	}
	if _, err := toml.Decode(string(data), &probe); err != nil {
		t.Fatalf("hub.toml unparsable after rewrite: %v\n%s", err, data)
	}
	record, ok := probe.HostRecords["alpha"]
	if !ok {
		t.Fatalf("rewrite dropped alpha's record entirely:\n%s", data)
	}
	for _, key := range []string{
		"bootstrap_attempted", "bootstrap_epoch_boot", "bootstrap_epoch_op_seq",
		"bootstrap_attempt_token", "helper_installed", "helper_version",
	} {
		if value, present := record[key]; present {
			t.Errorf("rewrite preserved the retired key %s = %v; want it dropped", key, value)
		}
	}
	if record["incarnation_id"] != "inc-alpha" || record["presence_epoch"] != int64(7) {
		t.Fatalf("alpha's record after rewrite = %#v, want its identity fields preserved", record)
	}
}
