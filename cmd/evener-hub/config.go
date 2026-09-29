package hub

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
)

var configUserHomeDir = os.UserHomeDir
var configReadFile = os.ReadFile

// ProviderConfig lists the models available from a single provider.
type ProviderConfig struct {
	Name   string   `toml:"name"`
	Models []string `toml:"models"`
}

// HostConfig is one [[hosts]] entry: a controller's connection entry for a
// remote hub. Name is the source ID used in refs and URLs; SSH is the
// destination passed to ssh (component 04), and User overrides its user.
// EvenerPath and Roots are advisory inputs to components 04/05.
type HostConfig struct {
	Name string `toml:"name"`
	SSH  string `toml:"ssh"`
	// User, and every field below it, is optional: omitempty is what the
	// machine-managed hub.toml writer relies on so a rewrite never invents a
	// key the operator (or the dialog that added the host) did not set. It has
	// no effect on decoding.
	User string `toml:"user,omitempty"`
	// EvenerPath is the host binary's absolute path, when it is not on PATH.
	EvenerPath string `toml:"evener_path,omitempty"`
	// ConfigPath is the host hub's hub.toml, when it is not at the default
	// location, so the bridge attaches with the host's own configuration.
	ConfigPath string `toml:"config_path,omitempty"`
	// Addr is the host hub's listen address, when it is not the default. The
	// manager needs it to restart the hub and to health-check it after a deploy,
	// where a wrong default would probe or kill the wrong listener.
	Addr  string   `toml:"addr,omitempty"`
	Roots []string `toml:"roots,omitempty"`
	// KeyPath is the SSH private-key file for this host, when it is not the
	// user's ssh_config default. The machine-managed hub.toml stores it (the
	// UI's Add/Edit dialog collects it), so a UI-added host's key path
	// round-trips through a rewrite (registry spec 08 §6).
	KeyPath string `toml:"key_path,omitempty"`
}

// Config is the hub's runtime configuration loaded from hub.toml (see
// DefaultConfigPath).
type Config struct {
	Addr               string           `toml:"addr"`
	MobileBaseURL      string           `toml:"mobile_base_url"`
	HubStateRoot       string           `toml:"hub_state_root"`
	StateGlob          string           `toml:"state_glob"`
	RunDir             string           `toml:"run_dir"`
	PastIndexDB        string           `toml:"past_index_db"`
	StatusPollInterval time.Duration    `toml:"status_poll_interval"`
	PastIndexRebuild   time.Duration    `toml:"past_index_rebuild_interval"`
	SpawnTimeout       time.Duration    `toml:"spawn_timeout"`
	PastResultsPerPage int              `toml:"past_results_per_page"`
	Providers          []ProviderConfig `toml:"providers"`
	Hosts              []HostConfig     `toml:"hosts"`
	// HostRecords and Generations are hub.toml's machine-managed per-host
	// records (registry spec 08 §6's reserved-key layout). They live beside
	// [[hosts]], never inside an entry, because they must survive a host edit:
	// an edit rewrites the entry's configured fields and nothing here. The
	// zero/absent value is the compatibility default for a file written before
	// the records existed — see app_host_records.go.
	HostRecords map[string]HostRecord     `toml:"host_records"`
	Generations map[string]HostGeneration `toml:"generations"`
	// MutationReceipts is hub.toml's durable mutation-receipt section (registry
	// spec 08 §5/§6), keyed by the five-part scoped receipt key. Like the two
	// records above it lives beside [[hosts]] and survives every host edit; the
	// absent value is the compatibility default for a file written before
	// receipts existed.
	MutationReceipts map[string]HostMutationReceipt `toml:"mutation_receipts"`
	// Tombstones is hub.toml's durable removed-host section (registry spec 08
	// §6/§15), keyed by host name. It lives beside [[hosts]]; a tombstone and
	// its removal are one atomic write, and the absent value is the
	// compatibility default for a file written before tombstones existed.
	Tombstones map[string]HostTombstone `toml:"tombstones"`
	// PrunedReceipts is hub.toml's durable pruned-marker section (registry spec
	// 08 §6), keyed by the full five-part scoped receipt key. Each marker is
	// the bounded proof that a superseded receipt was compacted away.
	PrunedReceipts map[string]PrunedReceiptMarker `toml:"pruned_receipts"`
	// StagedReceipts is hub.toml's durable staged-receipt marker section
	// (registry spec 08 §5), keyed by host name: the transient marker one
	// commit's step-(2) write carries until its post-commit write replaces it
	// with the finalized receipt. At most one marker per host.
	StagedReceipts map[string]HostStagedReceipt `toml:"staged_receipts"`
	// TeardownRemnants is hub.toml's durable teardown-remnant section (registry
	// spec 08 §6), keyed by the server-generated remnantId: the open remnants a
	// committed-with-teardown-failure left behind and the typed resolved records
	// their clearances wrote.
	TeardownRemnants map[string]HostTeardownRemnant `toml:"teardown_remnants"`
	// TeardownAttempts is hub.toml's durable teardown-attempt section (registry
	// spec 08 §6), keyed by the server-generated attempt id: the claim a retry
	// or recover writes beside the remnant it claims.
	TeardownAttempts map[string]HostTeardownAttempt `toml:"teardown_attempts"`
	// PendingStoreSync is hub.toml's cross-file commit intent section
	// (deploy-pipeline spec 08b §9), keyed by host name: the exact store rows a
	// commit's swap is deleting or invalidating, plus the hub.toml generation
	// the intent belongs to. The mutation's atomic write carries it, the store's
	// purge applies it after the swap, and a follow-up atomic write clears it.
	// At most one intent per host.
	PendingStoreSync map[string]HostStoreSyncIntent `toml:"pending_store_sync"`
	// MirrorCommits is hub.toml's generation-mirror commit-marker section
	// (deploy-pipeline spec 08b §4), keyed by host name: the (hub.toml
	// generation, store-mirror generation) pair every hub.toml commit that also
	// advances a mirrored store-side generation writes. Boot reconciles the
	// store mirror against the file's marks with these as the authorization
	// evidence.
	MirrorCommits map[string]HostMirrorCommit `toml:"mirror_commits"`

	// PluginAutoUpgrade is the global on/off switch for the background plugin
	// auto-upgrade daemon (design doc §9.1). Defaults to on: the meaningful
	// consent gate is the per-plugin `autoUpgrade` opt-in (SetAutoUpgrade) —
	// enabling that on an already-installed, git-backed plugin is the
	// standing consent for it to be upgraded unattended. This switch exists
	// as an operator-level kill switch/tuning knob, not the primary gate; if
	// it defaulted off, flipping a plugin's auto-upgrade toggle in the web/TUI
	// would silently do nothing until hub.toml was also hand-edited.
	PluginAutoUpgrade bool `toml:"plugin_auto_upgrade"`
	// PluginAutoUpgradeInterval is how often the daemon refreshes marketplaces
	// and re-checks autoUpgrade-enabled plugins, plus once on hub start.
	PluginAutoUpgradeInterval time.Duration `toml:"plugin_auto_upgrade_interval"`
	// DaemonIdleTimeout is how long a spawned daemon may sit continuously
	// idle before it retires itself. One hour by default; an explicit "0s"
	// disables automatic retirement and is NOT floored back to the default —
	// unlike the auto-upgrade interval, zero here is the documented kill
	// switch, not a panic risk. Negative values are rejected at load.
	DaemonIdleTimeout time.Duration `toml:"daemon_idle_timeout"`
	// APILog is the hub's default for durable API-request logging on the
	// evener serve daemons it spawns: true passes --api-log on, so every
	// provider request and response body is recorded to the session's
	// .api.jsonl. It is a floor, not a force: launch config layers that set
	// api_log explicitly (either direction) win over it. Defaults to false —
	// API-request logging is opt-in because its records are large.
	APILog bool `toml:"api_log"`
	// HostProbeTimeout is the owner-adjustable deadline for one
	// evener/host/running round trip (deploy pipeline 08b §6 step 2: the plan's
	// probe is "deadline-bounded with an explicit owner-adjustable probe
	// timeout"). It bounds the gate hold too: a hung remote holds no gate past
	// the probe window. Default DefaultHostProbeTimeout (10s); a value at or
	// below zero is floored back to the default at load, since an unset probe
	// deadline would let a hung remote hold the plan's gate.
	HostProbeTimeout time.Duration `toml:"host_probe_timeout"`
	// HostMinFreeSpaceBytes is the owner-adjustable minimum free space the
	// serving hub's running-health predicate requires on each durable state root
	// (deploy pipeline 08b §10). Below it the hub reports healthy: false without
	// running the state-root write probe. Default
	// DefaultHostMinFreeSpaceBytes (512 MiB); a value at or below zero is
	// floored back to the default at load, so a state root that is critically
	// full is never reported healthy by an unset knob.
	HostMinFreeSpaceBytes int64 `toml:"host_min_free_space_bytes"`
	// HostTombstoneRetention is the owner-set tombstone retention period
	// (registry spec 08 §15: "owner-set `tombstoneRetention` knob, default 7
	// days"): a tombstone past `removed_at + retention` is pruned from hub.toml
	// by the mutation-path atomic write and by boot, and omitted in memory by
	// `list` between those writes. Default DefaultHostTombstoneRetention; a
	// value at or below zero is floored back to the default at load.
	HostTombstoneRetention time.Duration `toml:"host_tombstone_retention"`
	// HostTombstoneMaxRows and HostTombstoneMaxRowBytes are the per-tombstone
	// retained-projection bounds (spec §15: "at most 500 retained rows per
	// tombstone and at most 1 MiB of serialized row bytes per tombstone
	// (owner-adjustable knobs in the same family as the cleared-marker TTL; the
	// defaults ship in the implementing PR)"). A non-positive value is floored
	// to the default at load, so an unset knob never disables the bound.
	HostTombstoneMaxRows     int   `toml:"host_tombstone_max_rows"`
	HostTombstoneMaxRowBytes int64 `toml:"host_tombstone_max_row_bytes"`
	// HostTombstoneMaxCount and HostTombstoneMaxBytes are the GLOBAL tombstone
	// caps (spec §15: "at most 64 tombstones and at most 16 MiB of total
	// serialized tombstone bytes (same knob family; defaults ship in the
	// implementing PR)"). A non-positive value is floored to the default.
	HostTombstoneMaxCount int   `toml:"host_tombstone_max_count"`
	HostTombstoneMaxBytes int64 `toml:"host_tombstone_max_bytes"`
	// HostOperationTerminalPerHost, HostOperationTerminalStoreWide,
	// HostOperationStoreMaxBytes, HostOperationTerminalMaxAge and
	// HostOperationTombstonesPerHost are the operation store's retention knobs
	// (deploy pipeline 08b §4: "at most 50 terminal records per host (tunable
	// owner knob ...) ... at most 500 terminal records store-wide, at most
	// 64 MiB of serialized store bytes, and at most 30 days of terminal-record
	// age ... At most 50 tombstones per host (same owner-knob family)"). A
	// non-positive value is floored to the default at load. The removed-host
	// horizon is HostTombstoneRetention itself: §4's "past the
	// `tombstoneRetention` horizon — 7-day default".
	HostOperationTerminalPerHost   int           `toml:"host_operation_terminal_per_host"`
	HostOperationTerminalStoreWide int           `toml:"host_operation_terminal_store_wide"`
	HostOperationStoreMaxBytes     int64         `toml:"host_operation_store_max_bytes"`
	HostOperationTerminalMaxAge    time.Duration `toml:"host_operation_terminal_max_age"`
	HostOperationTombstonesPerHost int           `toml:"host_operation_tombstones_per_host"`
	// HostSupersededReceiptMaxCount and HostSupersededReceiptTTL bound a live
	// name's superseded-generation receipts (spec §6: "at most 8 newest
	// same-key superseded receipts per name (owner-adjustable count bound; a
	// same-key superseded receipt older than the owner-set superseded-receipt
	// TTL compacts the same way)"). A non-positive value is floored to the
	// default at load.
	HostSupersededReceiptMaxCount int           `toml:"host_superseded_receipt_max_count"`
	HostSupersededReceiptTTL      time.Duration `toml:"host_superseded_receipt_ttl"`
	// HostPrunedReceiptMaxCount and HostPrunedReceiptTTL bound a live name's
	// pruned markers (spec §6: "at most 64 newest markers per live name plus a
	// marker TTL in the same owner-knob family as the superseded-receipt TTL
	// (every `hub.toml` mutation and every boot compacts markers past either
	// bound in the same atomic write; defaults ship in the implementing PR)").
	// A non-positive value is floored to the default at load.
	HostPrunedReceiptMaxCount int           `toml:"host_pruned_receipt_max_count"`
	HostPrunedReceiptTTL      time.Duration `toml:"host_pruned_receipt_ttl"`
	// HostKeylessAuditMaxCount and HostKeylessAuditTTL bound a name's
	// keyless-add audit records (spec §11: "audit records compact under the
	// same dual bound as receipts — at most 64 newest per name plus an
	// owner-set audit TTL in the same knob family, every `hub.toml` mutation
	// and every boot compacting past either bound in the same atomic write").
	// A non-positive value is floored to the default at load.
	HostKeylessAuditMaxCount int           `toml:"host_keyless_audit_max_count"`
	HostKeylessAuditTTL      time.Duration `toml:"host_keyless_audit_ttl"`
	// The teardown-repair bounds (registry spec 08 §6): the cleared-remnant
	// marker bounds, the recovery-marker bounds, and the attempt-history bound
	// per remnant. Non-positive values take the documented defaults.
	HostRemnantClearedMaxCount  int           `toml:"host_remnant_cleared_max_count"`
	HostRemnantClearedTTL       time.Duration `toml:"host_remnant_cleared_ttl"`
	HostRemnantRecoveryMaxCount int           `toml:"host_remnant_recovery_max_count"`
	HostRemnantRecoveryTTL      time.Duration `toml:"host_remnant_recovery_ttl"`
	HostRemnantAttemptMaxCount  int           `toml:"host_remnant_attempt_max_count"`
	HostRemnantTeardownTimeout  time.Duration `toml:"host_remnant_teardown_timeout"`
	HostRemnantEscalationAge    time.Duration `toml:"host_remnant_escalation_age"`
}

const (
	// DefaultHostProbeTimeout is the default deadline for one
	// evener/host/running round trip. Ten seconds is generous for a loopback
	// AppWire call proxied over the SSH bridge — the probe's own work is a
	// roster read, a free-space query, and one tiny state-root write — while
	// keeping the plan's gate hold bounded when a remote hangs.
	DefaultHostProbeTimeout = 10 * time.Second
	// DefaultHostMinFreeSpaceBytes is the default minimum free space the
	// running-health predicate requires on each durable state root. 512 MiB is
	// roughly the headroom a hub needs to keep writing session state and its
	// own durable stores without landing in a critically-full window, and it is
	// far above the size of any single probe write.
	DefaultHostMinFreeSpaceBytes int64 = 512 << 20
	// DefaultHostTombstoneRetention is the tombstone retention period spec 08
	// §15 names: seven days.
	DefaultHostTombstoneRetention = 7 * 24 * time.Hour
	// DefaultHostTombstoneMaxRows and DefaultHostTombstoneMaxRowBytes are the
	// per-tombstone retained-projection bounds spec §15 names: 500 rows and
	// 1 MiB of serialized row bytes.
	DefaultHostTombstoneMaxRows           = 500
	DefaultHostTombstoneMaxRowBytes int64 = 1 << 20
	// DefaultHostTombstoneMaxCount and DefaultHostTombstoneMaxBytes are the
	// global caps spec §15 names: 64 tombstones and 16 MiB of serialized
	// tombstone bytes.
	DefaultHostTombstoneMaxCount       = 64
	DefaultHostTombstoneMaxBytes int64 = 16 << 20
	// DefaultHostOperationTerminalPerHost, DefaultHostOperationTerminalStoreWide,
	// DefaultHostOperationStoreMaxBytes, DefaultHostOperationTerminalMaxAge and
	// DefaultHostOperationTombstonesPerHost are deploy pipeline 08b §4's
	// shipped operation-store retention defaults: 50 terminal records per host,
	// 500 store-wide, 64 MiB of serialized store bytes, 30 days of terminal
	// age, and 50 dedup tombstones per host.
	DefaultHostOperationTerminalPerHost         = 50
	DefaultHostOperationTerminalStoreWide       = 500
	DefaultHostOperationStoreMaxBytes     int64 = 64 << 20
	DefaultHostOperationTerminalMaxAge          = 30 * 24 * time.Hour
	DefaultHostOperationTombstonesPerHost       = 50
	// DefaultHostSupersededReceiptMaxCount and DefaultHostSupersededReceiptTTL
	// are the superseded-receipt bounds spec §6 describes. The TTL matches the
	// tombstone retention: a superseded receipt's post-remove recovery window
	// is the tombstone's own window, and any replay past it refuses through the
	// retained pruned marker instead.
	DefaultHostSupersededReceiptMaxCount = 8
	DefaultHostSupersededReceiptTTL      = 7 * 24 * time.Hour
	// DefaultHostPrunedReceiptMaxCount and DefaultHostPrunedReceiptTTL are the
	// pruned-marker bounds spec §6 describes; the TTL matches the tombstone
	// retention for the same reason.
	DefaultHostPrunedReceiptMaxCount = 64
	DefaultHostPrunedReceiptTTL      = 7 * 24 * time.Hour
	// DefaultHostKeylessAuditMaxCount and DefaultHostKeylessAuditTTL are the
	// keyless-add audit bounds spec §11 describes; the TTL is longer than the
	// tombstone window because the audit trail's purpose is post-hoc forensics
	// rather than outcome recovery.
	DefaultHostKeylessAuditMaxCount = 64
	DefaultHostKeylessAuditTTL      = 30 * 24 * time.Hour
	// DefaultHostRemnantClearedMaxCount and DefaultHostRemnantClearedTTL are the
	// cleared-remnant marker bounds spec §6 names: "owner-set cleared-marker TTL
	// plus an at-most-64-newest-per-name count bound in the same owner-knob
	// family". The TTL matches the tombstone retention so a lost-response retry
	// keeps its `already-cleared` replay for the window the tombstone lives, and
	// past it reads `teardown-unknown-key` exactly as the spec requires.
	DefaultHostRemnantClearedMaxCount = 64
	DefaultHostRemnantClearedTTL      = 7 * 24 * time.Hour
	// DefaultHostRemnantRecoveryMaxCount and DefaultHostRemnantRecoveryTTL are
	// the recovery-marker bounds spec §11 names: "at most 64 newest recovery
	// records per name plus a recovery-marker TTL in the same owner-knob family
	// as the cleared-marker TTL".
	DefaultHostRemnantRecoveryMaxCount = 64
	DefaultHostRemnantRecoveryTTL      = 7 * 24 * time.Hour
	// DefaultHostRemnantAttemptMaxCount bounds the attempt history one remnant
	// keeps: the newest attempts survive so a timed-out attempt's record stays
	// readable while a churn of retries cannot grow hub.toml without limit.
	DefaultHostRemnantAttemptMaxCount = 8
	// DefaultHostRemnantTeardownTimeout is the retry's bounded execution
	// deadline (spec §6: "the retry's own teardown run carries a bounded
	// execution deadline of the same owner-set family"). Two minutes is long
	// enough for a local supervisor/channel teardown to drain and short enough
	// that a stuck remote surfaces the terminal outcome with a live retry handle
	// instead of holding the host gate.
	DefaultHostRemnantTeardownTimeout = 2 * time.Minute
	// DefaultHostRemnantEscalationAge is the bound past which an open remnant's
	// row reports `escalationAgeSec` (spec §11): an hour is long enough that a
	// transient teardown failure resolves itself through one retry and short
	// enough that an operator hears about a remnant that is not going away.
	DefaultHostRemnantEscalationAge = time.Hour
)

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Addr:                           "127.0.0.1:9180",
		HubStateRoot:                   DefaultHubStateRoot(),
		StateGlob:                      "",
		RunDir:                         "",
		StatusPollInterval:             2 * time.Second,
		PastIndexRebuild:               60 * time.Second,
		SpawnTimeout:                   30 * time.Second,
		PastResultsPerPage:             50,
		PluginAutoUpgrade:              true,
		PluginAutoUpgradeInterval:      12 * time.Hour,
		DaemonIdleTimeout:              time.Hour,
		HostProbeTimeout:               DefaultHostProbeTimeout,
		HostMinFreeSpaceBytes:          DefaultHostMinFreeSpaceBytes,
		HostTombstoneRetention:         DefaultHostTombstoneRetention,
		HostTombstoneMaxRows:           DefaultHostTombstoneMaxRows,
		HostTombstoneMaxRowBytes:       DefaultHostTombstoneMaxRowBytes,
		HostTombstoneMaxCount:          DefaultHostTombstoneMaxCount,
		HostTombstoneMaxBytes:          DefaultHostTombstoneMaxBytes,
		HostOperationTerminalPerHost:   DefaultHostOperationTerminalPerHost,
		HostOperationTerminalStoreWide: DefaultHostOperationTerminalStoreWide,
		HostOperationStoreMaxBytes:     DefaultHostOperationStoreMaxBytes,
		HostOperationTerminalMaxAge:    DefaultHostOperationTerminalMaxAge,
		HostOperationTombstonesPerHost: DefaultHostOperationTombstonesPerHost,
		HostSupersededReceiptMaxCount:  DefaultHostSupersededReceiptMaxCount,
		HostSupersededReceiptTTL:       DefaultHostSupersededReceiptTTL,
		HostPrunedReceiptMaxCount:      DefaultHostPrunedReceiptMaxCount,
		HostPrunedReceiptTTL:           DefaultHostPrunedReceiptTTL,
		HostKeylessAuditMaxCount:       DefaultHostKeylessAuditMaxCount,
		HostKeylessAuditTTL:            DefaultHostKeylessAuditTTL,
		HostRemnantClearedMaxCount:     DefaultHostRemnantClearedMaxCount,
		HostRemnantClearedTTL:          DefaultHostRemnantClearedTTL,
		HostRemnantRecoveryMaxCount:    DefaultHostRemnantRecoveryMaxCount,
		HostRemnantRecoveryTTL:         DefaultHostRemnantRecoveryTTL,
		HostRemnantAttemptMaxCount:     DefaultHostRemnantAttemptMaxCount,
		HostRemnantTeardownTimeout:     DefaultHostRemnantTeardownTimeout,
		HostRemnantEscalationAge:       DefaultHostRemnantEscalationAge,
	}
}

// DefaultHubStateRoot returns the hub's machine-generated state root
// (cmdutil.DefaultStateRoot(): $XDG_STATE_HOME/evener, or
// ~/.local/state/evener when XDG_STATE_HOME is unset). It holds the auth
// token, the past-session index, the deletion-fence store, the host lock,
// and the daemon rendezvous/log directory.
func DefaultHubStateRoot() string {
	return cmdutil.DefaultStateRoot()
}

// DefaultConfigPath returns the hub's optional config file:
// cmdutil.DefaultConfigRoot()/hub.toml ($XDG_CONFIG_HOME/evener/hub.toml, or
// ~/.config/evener/hub.toml when XDG_CONFIG_HOME is unset).
func DefaultConfigPath() string {
	return filepath.Join(cmdutil.DefaultConfigRoot(), "hub.toml")
}

// DefaultStateGlob returns the project state roots indexed by the hub.
func DefaultStateGlob() string {
	base := envvars.XDGStateHome.Getenv()
	if base == "" {
		home, err := configUserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "evener", "projects", "*")
}

// DefaultPastIndexDBPath returns DefaultHubStateRoot()/index.db.
func DefaultPastIndexDBPath() string {
	return filepath.Join(DefaultHubStateRoot(), "index.db")
}

// LoadConfig reads path. A missing file returns DefaultConfig() and a nil
// error: that fallback is the implicit default-path contract
// (DefaultConfigPath), which a hub started without --config relies on. A file
// that exists but cannot be read or parsed is an error.
//
// An operator-named path must go through LoadConfigExplicit: silently
// defaulting a typo'd --config would start the hub against the wrong state
// root, address, and host list.
func LoadConfig(path string) (Config, error) {
	return loadConfig(path, false)
}

// LoadConfigExplicit reads a path an operator named (--config). Unlike
// LoadConfig, a missing file is an error naming the path rather than a silent
// DefaultConfig() fallback.
func LoadConfigExplicit(path string) (Config, error) {
	return loadConfig(path, true)
}

// loadConfigForCommandLine loads cfgPath the way the flag layer resolved it:
// explicit is true when the operator named the path with --config, which makes
// a missing file a startup refusal instead of a default.
func loadConfigForCommandLine(cfgPath string, explicit bool) (Config, error) {
	if explicit {
		return LoadConfigExplicit(cfgPath)
	}
	return LoadConfig(cfgPath)
}

func loadConfig(path string, explicit bool) (Config, error) {
	data, err := configReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return DefaultConfig(), nil
		}
		return DefaultConfig(), fmt.Errorf("read config %s: %w", path, err)
	}
	return decodeConfig(path, string(data))
}

// decodeConfig decodes and validates one hub.toml document. loadConfig runs it
// on the file it read; the host-management surface's in-place rewrite runs it
// on the bytes it is about to write, so a hub.toml the hub writes is one the
// hub can read back at boot. name is the path the error messages name.
func decodeConfig(name, data string) (Config, error) {
	cfg := DefaultConfig()
	metadata, decodeErr := toml.Decode(data, &cfg)
	for _, key := range []string{"codex_sources", "codex_launches"} {
		if metadata.IsDefined(key) {
			return cfg, fmt.Errorf("config section %q is no longer supported because Codex agent integration has been removed; remove it from hub.toml", key)
		}
	}
	if decodeErr != nil {
		return cfg, fmt.Errorf("parse config %s: %w", name, decodeErr)
	}
	// The retired crash-fencing bootstrap keys decode into HostRecord's retired
	// fields (the type documents them) so a hub.toml the bootstrap build wrote
	// still loads: the reserved-record rule below refuses a reserved field this
	// build does not decode, and the removed first-contact caller persisted those
	// keys. Drop them from the loaded document here, so no in-memory record
	// carries them and every rewrite derived from this config re-emits the
	// record without them — the next write drops the keys, rather than the
	// preservation rule keeping them alive forever.
	for hostName, record := range cfg.HostRecords {
		cfg.HostRecords[hostName] = record.withoutRetiredFencingFields()
	}
	// DaemonIdleTimeout is a duration STRING. BurntSushi/toml decodes a bare
	// integer as a nanosecond count without error, so `daemon_idle_timeout =
	// 3600` (plausible shorthand for one hour) would silently arm a 3.6µs idle
	// deadline and churn retire/resume on every spawned daemon. Reject the
	// integer form instead of flooring it: the doc comment defines an explicit
	// "0s" as the deliberate kill switch, which flooring would rewrite. The
	// metadata's TOML type is the parser's own discriminator (verified against
	// BurntSushi/toml v1.6.0: bare integers report "Integer", quoted duration
	// strings report "String", an absent key reports "").
	if metadata.Type("daemon_idle_timeout") == "Integer" {
		return cfg, fmt.Errorf("daemon_idle_timeout must be a duration string such as \"1h\" or \"0s\" (got the integer %[1]d, which TOML decodes as %[1]d nanoseconds)", int64(cfg.DaemonIdleTimeout))
	}
	// The same footgun, same refusal: a bare integer is a nanosecond count, so
	// `host_probe_timeout = 10` (plausible shorthand for ten seconds) would arm
	// a 10ns deadline that fails every probe.
	if metadata.Type("host_probe_timeout") == "Integer" {
		return cfg, fmt.Errorf("host_probe_timeout must be a duration string such as \"10s\" (got the integer %[1]d, which TOML decodes as %[1]d nanoseconds)", int64(cfg.HostProbeTimeout))
	}
	// The same footgun, same refusal, for every duration-shaped record knob:
	// `host_tombstone_retention = 7` would arm a 7ns retention and prune every
	// tombstone on the next write.
	for _, knob := range []struct {
		key   string
		value time.Duration
	}{
		{"host_tombstone_retention", cfg.HostTombstoneRetention},
		{"host_operation_terminal_max_age", cfg.HostOperationTerminalMaxAge},
		{"host_superseded_receipt_ttl", cfg.HostSupersededReceiptTTL},
		{"host_pruned_receipt_ttl", cfg.HostPrunedReceiptTTL},
		{"host_keyless_audit_ttl", cfg.HostKeylessAuditTTL},
	} {
		if metadata.Type(knob.key) == "Integer" {
			return cfg, fmt.Errorf("%[1]s must be a duration string such as \"168h\" (got the integer %[2]d, which TOML decodes as %[2]d nanoseconds)", knob.key, int64(knob.value))
		}
	}
	applyConfigDefaults(&cfg)
	if cfg.DaemonIdleTimeout < 0 {
		return cfg, fmt.Errorf("daemon_idle_timeout must not be negative (got %v)", cfg.DaemonIdleTimeout)
	}
	if err := validateHostConfigs(cfg.Hosts); err != nil {
		return cfg, fmt.Errorf("validate hosts: %w", err)
	}
	// The machine records are decoded as plain tables, so a record the hub's
	// writer never emits — an incomplete one, or a live record disagreeing with
	// the name's high-water triple — is refused here rather than read as one of
	// its halves (registry spec 08 §6: a reserved value whose shape this build
	// cannot decode is refused loudly before any rewrite).
	if err := validateHostRecords(cfg.HostRecords, cfg.Generations); err != nil {
		return cfg, fmt.Errorf("validate host records: %w", err)
	}
	// The mutation receipts are the other machine-managed section this build
	// decodes: a record whose key or shape this build cannot decode, or whose
	// fields disagree with its key, is refused loudly before any rewrite
	// (registry spec 08 §6's reserved-namespace rule) — never read as a
	// half-understood record, and never dropped by the next rewrite.
	if err := validateHostMutationReceipts(cfg.MutationReceipts); err != nil {
		return cfg, fmt.Errorf("validate mutation receipts: %w", err)
	}
	// The tombstones and pruned markers are the other machine-managed sections
	// this build decodes: a record whose shape this build cannot decode, or a
	// tombstone whose entry/identity fails validation, is refused loudly before
	// any rewrite (registry spec 08 §6's reserved-namespace rule) — the same
	// hard startup-error posture the host entries take.
	if err := validateHostTombstones(cfg.Tombstones, cfg.Generations); err != nil {
		return cfg, fmt.Errorf("validate tombstones: %w", err)
	}
	if err := validatePrunedReceipts(cfg.PrunedReceipts); err != nil {
		return cfg, fmt.Errorf("validate pruned receipts: %w", err)
	}
	// The teardown-repair records (registry spec 08 §5/§6) are the sections this
	// slice adds: the staged-receipt markers a commit's step-(2) write carries,
	// the open remnants and typed resolved records their clearances leave, and
	// the attempt records a retry claims. Each is refused loudly when its shape
	// is one this build cannot decode, so a marker or remnant that reached the
	// file from a newer build is a hard startup error rather than a silently
	// reinterpreted (or dropped) record.
	if err := validateHostStagedReceipts(cfg.StagedReceipts); err != nil {
		return cfg, fmt.Errorf("validate staged receipts: %w", err)
	}
	if err := validateHostTeardownRemnants(cfg.TeardownRemnants); err != nil {
		return cfg, fmt.Errorf("validate teardown remnants: %w", err)
	}
	if err := validateHostTeardownAttempts(cfg.TeardownAttempts, cfg.TeardownRemnants); err != nil {
		return cfg, fmt.Errorf("validate teardown attempts: %w", err)
	}
	// The cross-file commit intent and the generation-mirror markers are the
	// deploy pipeline's machine-managed sections (08b §4/§9): a record whose
	// shape this build cannot decode — an intent without rows or a generation,
	// a marker without a generation — is refused loudly before any rewrite, the
	// same reserved-namespace posture the sections above take.
	if err := validateHostStoreSync(cfg.PendingStoreSync); err != nil {
		return cfg, fmt.Errorf("validate pending store sync: %w", err)
	}
	if err := validateHostMirrorCommits(cfg.MirrorCommits); err != nil {
		return cfg, fmt.Errorf("validate mirror commits: %w", err)
	}
	// Every field of a reserved record must decode: the two tables are decoded
	// into typed structs and rebuilt on every rewrite, so a field this build does
	// not know would be silently dropped by the next write. Spec 08 §6 is
	// explicit that forward preservation is not offered — "a reserved value whose
	// shape this build cannot decode is refused loudly before any rewrite" — and
	// a downgrade meeting a newer record must refuse to rewrite rather than
	// preserve or drop it. Unknown keys outside the reserved set stay the
	// operator's data and are not this check's business.
	for _, key := range metadata.Undecoded() {
		if len(key) > 1 && (key[0] == "host_records" || key[0] == "generations" || key[0] == "mutation_receipts" ||
			key[0] == "tombstones" || key[0] == "pruned_receipts" || key[0] == "staged_receipts" ||
			key[0] == "teardown_remnants" || key[0] == "teardown_attempts" ||
			key[0] == "pending_store_sync" || key[0] == "mirror_commits") {
			return cfg, fmt.Errorf("config %s: reserved record %s carries a field this build does not decode; refusing rather than dropping it on the next rewrite", name, key.String())
		}
	}
	if err := validateMobileBaseURL(cfg.MobileBaseURL); err != nil {
		return cfg, fmt.Errorf("validate mobile_base_url: %w", err)
	}
	return cfg, nil
}

// The host-entry refusals spec 03 ("config_path / addr") assigns to
// validateHostConfigs. They are named sentinels so tests and callers can match
// them with errors.Is.
var (
	// ErrHostAddrPair marks a [[hosts]] entry that sets exactly one of
	// config_path and addr. The pair describes one host hub layout — the bridge
	// resolves the hub's address and token from config_path, while the SSH
	// manager's restart and health path probes addr — so a half-specified entry
	// would attach through one layout while the manager probes another.
	ErrHostAddrPair = errors.New("host config_path and addr must be set together")
	// ErrHostAddr marks an addr the controller cannot use: malformed, missing a
	// usable port, or not a loopback/wildcard bind. addr is interpolated into an
	// SSH-side health probe and restart identity check, so a non-loopback value
	// could let those paths reach an arbitrary host; the wildcard spellings are
	// accepted because the manager normalizes them to loopback for its own dial
	// and probe.
	ErrHostAddr = errors.New("host addr unusable")
)

// validateHostConfigs normalizes and validates the [[hosts]] list by building a
// throwaway registry, so name grammar, duplicate names, reserved "local", the
// ".." rule, ssh presence, the user/ssh-user conflict, and empty roots are all
// checked by the single source of truth in hostreg. Each entry also runs
// validateHostEntry — the pair and addr rules spec 03's "config_path / addr"
// contract requires, the same function the runtime add/update paths run — so a
// hub.toml entry and a UI-added entry are validated by one code path. No host
// is registered anywhere: this is pure validation.
//
// It rewrites hosts IN PLACE with the normalized values. hostreg trims before it
// stores, so validating a copy would let ssh = "  m4.local  " pass here and then
// reach consumers untrimmed — exactly the unresolvable-host hazard the registry
// normalization exists to prevent.
func validateHostConfigs(hosts []HostConfig) error {
	if len(hosts) == 0 {
		return nil
	}
	entries := make([]hostreg.Host, 0, len(hosts))
	for i, h := range hosts {
		entry := hostreg.Normalize(hostreg.Host{
			Name:       h.Name,
			SSH:        h.SSH,
			User:       h.User,
			EvenerPath: h.EvenerPath,
			ConfigPath: h.ConfigPath,
			Addr:       h.Addr,
			Roots:      h.Roots,
			KeyPath:    h.KeyPath,
		})
		if err := validateHostEntry(entry); err != nil {
			return err
		}
		hosts[i].SSH = entry.SSH
		hosts[i].Name = entry.Name
		hosts[i].User = entry.User
		hosts[i].EvenerPath = entry.EvenerPath
		hosts[i].ConfigPath = entry.ConfigPath
		hosts[i].Addr = entry.Addr
		hosts[i].Roots = entry.Roots
		hosts[i].KeyPath = entry.KeyPath
		entries = append(entries, entry)
	}
	_, err := hostreg.New(entries)
	return err
}

// validateHostEntry runs every entry check the host surfaces share: hostreg's
// own shape validation (name grammar, reserved name, ssh destination, user/ssh
// agreement, non-empty roots) plus the config_path/addr pair and addr host
// rules hub.toml loading applies (validateHostAddr). hub.toml loading and the
// runtime evener/host/add and evener/host/update paths all call it, so an entry
// one surface refuses can never be stored by another.
func validateHostEntry(entry hostreg.Host) error {
	if err := hostreg.ValidateEntry(entry); err != nil {
		return err
	}
	return validateHostAddr(entry)
}

// validateHostAddr refuses an entry whose config_path/addr pair is
// half-specified, or whose addr the controller cannot use. It checks the
// normalized values the registry would store.
//
// The pair describes ONE host hub layout (component 04, "Address, config path,
// token"): the bridge resolves the hub's listen address and token state root
// from config_path, while the manager's restart and health path probes addr. A
// half-specified entry attaches through one layout while the manager probes
// another — a silent split-brain, not a clean failure.
//
// addr is interpolated into an SSH-side curl probe and the restart identity
// check, so a non-loopback value could let those paths reach an arbitrary
// reachable host. The address half delegates to sshconn.ValidateHubAddrShape —
// the SSH probe's own accepted-address rule — and wraps its refusal in this
// package's ErrHostAddr, so load-time validation and the use-time probe cannot
// drift on what an address may be: a loopback host literal, "localhost", the
// wildcard spellings the manager normalizes to loopback (0.0.0.0, ::, and the
// empty host of ":port"), and a usable port.
func validateHostAddr(entry hostreg.Host) error {
	if (entry.ConfigPath == "") != (entry.Addr == "") {
		return fmt.Errorf("%w: host %q sets exactly one of config_path and addr; set both or neither", ErrHostAddrPair, entry.Name)
	}
	if entry.Addr == "" {
		return nil
	}
	if err := sshconn.ValidateHubAddrShape(entry.Addr); err != nil {
		return fmt.Errorf("%w: host %q %w", ErrHostAddr, entry.Name, err)
	}
	return nil
}

// validateMobileBaseURL accepts only an HTTP(S) origin. The pairing endpoint
// appends the capability path and token itself, so a configured path or query
// would either be discarded or accidentally extend the public contract.
func validateMobileBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("must be an http(s) origin: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("must use http or https")
	}
	if u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return errors.New("must be an origin without userinfo")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("must not include a path")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return errors.New("must not include a query")
	}
	if u.Fragment != "" {
		return errors.New("must not include a fragment")
	}
	if strings.HasSuffix(u.Host, ":") {
		return errors.New("port must not be empty")
	}
	if port := u.Port(); port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return errors.New("port must be between 1 and 65535")
		}
	}
	return nil
}

func applyConfigDefaults(cfg *Config) {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:9180"
	}
	if cfg.StatusPollInterval == 0 {
		cfg.StatusPollInterval = 2 * time.Second
	}
	if cfg.PastIndexRebuild == 0 {
		cfg.PastIndexRebuild = 60 * time.Second
	}
	if cfg.SpawnTimeout == 0 {
		cfg.SpawnTimeout = 30 * time.Second
	}
	if cfg.PastResultsPerPage == 0 {
		cfg.PastResultsPerPage = 50
	}
	if cfg.HostProbeTimeout <= 0 {
		cfg.HostProbeTimeout = DefaultHostProbeTimeout
	}
	if cfg.HostMinFreeSpaceBytes <= 0 {
		cfg.HostMinFreeSpaceBytes = DefaultHostMinFreeSpaceBytes
	}
	if cfg.HostTombstoneRetention <= 0 {
		cfg.HostTombstoneRetention = DefaultHostTombstoneRetention
	}
	if cfg.HostTombstoneMaxRows <= 0 {
		cfg.HostTombstoneMaxRows = DefaultHostTombstoneMaxRows
	}
	if cfg.HostTombstoneMaxRowBytes <= 0 {
		cfg.HostTombstoneMaxRowBytes = DefaultHostTombstoneMaxRowBytes
	}
	if cfg.HostTombstoneMaxCount <= 0 {
		cfg.HostTombstoneMaxCount = DefaultHostTombstoneMaxCount
	}
	if cfg.HostTombstoneMaxBytes <= 0 {
		cfg.HostTombstoneMaxBytes = DefaultHostTombstoneMaxBytes
	}
	if cfg.HostOperationTerminalPerHost <= 0 {
		cfg.HostOperationTerminalPerHost = DefaultHostOperationTerminalPerHost
	}
	if cfg.HostOperationTerminalStoreWide <= 0 {
		cfg.HostOperationTerminalStoreWide = DefaultHostOperationTerminalStoreWide
	}
	if cfg.HostOperationStoreMaxBytes <= 0 {
		cfg.HostOperationStoreMaxBytes = DefaultHostOperationStoreMaxBytes
	}
	if cfg.HostOperationTerminalMaxAge <= 0 {
		cfg.HostOperationTerminalMaxAge = DefaultHostOperationTerminalMaxAge
	}
	if cfg.HostOperationTombstonesPerHost <= 0 {
		cfg.HostOperationTombstonesPerHost = DefaultHostOperationTombstonesPerHost
	}
	if cfg.HostSupersededReceiptMaxCount <= 0 {
		cfg.HostSupersededReceiptMaxCount = DefaultHostSupersededReceiptMaxCount
	}
	if cfg.HostSupersededReceiptTTL <= 0 {
		cfg.HostSupersededReceiptTTL = DefaultHostSupersededReceiptTTL
	}
	if cfg.HostPrunedReceiptMaxCount <= 0 {
		cfg.HostPrunedReceiptMaxCount = DefaultHostPrunedReceiptMaxCount
	}
	if cfg.HostPrunedReceiptTTL <= 0 {
		cfg.HostPrunedReceiptTTL = DefaultHostPrunedReceiptTTL
	}
	if cfg.HostKeylessAuditMaxCount <= 0 {
		cfg.HostKeylessAuditMaxCount = DefaultHostKeylessAuditMaxCount
	}
	if cfg.HostKeylessAuditTTL <= 0 {
		cfg.HostKeylessAuditTTL = DefaultHostKeylessAuditTTL
	}
	if cfg.HubStateRoot == "" {
		cfg.HubStateRoot = DefaultHubStateRoot()
	}
	// time.NewTicker (app_plugin_autoupgrade.go) panics on d <= 0, and the
	// daemon is launched with a bare `go` (no recover), so a bad interval
	// would crash the whole hub. BurntSushi/toml happily parses a negative
	// duration string ("-1h" -> -1h, no error) and a bare integer as a
	// nanosecond count (12 -> 12ns, no error) — neither trips a `== 0` guard,
	// so this must be <= 0, not == 0. A positive-but-tiny value (that bare
	// `12`) would otherwise busy-loop the daemon, so it's also floored.
	if cfg.PluginAutoUpgradeInterval <= 0 {
		cfg.PluginAutoUpgradeInterval = 12 * time.Hour
	} else if cfg.PluginAutoUpgradeInterval < time.Minute {
		cfg.PluginAutoUpgradeInterval = time.Minute
	}
	// PluginAutoUpgrade is intentionally NOT defaulted here: it is a bool
	// whose zero value (false) is a legitimate explicit choice (an operator
	// opting out), indistinguishable at this point from "absent from the
	// file". DefaultConfig() already pre-populates true, and toml.Unmarshal
	// only overwrites keys actually present in the document, so an absent key
	// correctly leaves the true default and an explicit `false` correctly
	// sticks.
}
