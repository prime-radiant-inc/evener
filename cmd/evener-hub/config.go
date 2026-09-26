package hub

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
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
	User string `toml:"user"`
	// EvenerPath is the host binary's absolute path, when it is not on PATH.
	EvenerPath string `toml:"evener_path"`
	// ConfigPath is the host hub's hub.toml, when it is not at the default
	// location, so the bridge attaches with the host's own configuration.
	ConfigPath string `toml:"config_path"`
	// Addr is the host hub's listen address, when it is not the default. The
	// manager needs it to restart the hub and to health-check it after a deploy,
	// where a wrong default would probe or kill the wrong listener.
	Addr  string   `toml:"addr"`
	Roots []string `toml:"roots"`
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
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Addr:                      "127.0.0.1:9180",
		HubStateRoot:              DefaultHubStateRoot(),
		StateGlob:                 "",
		RunDir:                    "",
		StatusPollInterval:        2 * time.Second,
		PastIndexRebuild:          60 * time.Second,
		SpawnTimeout:              30 * time.Second,
		PastResultsPerPage:        50,
		PluginAutoUpgrade:         true,
		PluginAutoUpgradeInterval: 12 * time.Hour,
		DaemonIdleTimeout:         time.Hour,
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
	cfg := DefaultConfig()
	data, err := configReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	metadata, decodeErr := toml.Decode(string(data), &cfg)
	for _, key := range []string{"codex_sources", "codex_launches"} {
		if metadata.IsDefined(key) {
			return cfg, fmt.Errorf("config section %q is no longer supported because Codex agent integration has been removed; remove it from hub.toml", key)
		}
	}
	if decodeErr != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, decodeErr)
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
	applyConfigDefaults(&cfg)
	if cfg.DaemonIdleTimeout < 0 {
		return cfg, fmt.Errorf("daemon_idle_timeout must not be negative (got %v)", cfg.DaemonIdleTimeout)
	}
	if err := validateHostConfigs(cfg.Hosts); err != nil {
		return cfg, fmt.Errorf("validate hosts: %w", err)
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
// reachable host. Only a loopback host literal, "localhost", the wildcard
// spellings the manager normalizes to loopback (0.0.0.0, ::, and the empty host
// of ":port"), and a usable port may pass. The accepted set mirrors
// sshconn.validateHubAddr, which is the use-time backstop for an entry that
// reaches the manager without loading from hub.toml (a registry add), so
// load-time and probe-time agree on what an address may be.
func validateHostAddr(entry hostreg.Host) error {
	if (entry.ConfigPath == "") != (entry.Addr == "") {
		return fmt.Errorf("%w: host %q sets exactly one of config_path and addr; set both or neither", ErrHostAddrPair, entry.Name)
	}
	if entry.Addr == "" {
		return nil
	}
	hostPart, port, err := net.SplitHostPort(entry.Addr)
	if err != nil {
		return fmt.Errorf("%w: host %q addr %q is not host:port", ErrHostAddr, entry.Name, entry.Addr)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%w: host %q addr %q has no usable port", ErrHostAddr, entry.Name, entry.Addr)
	}
	if ip := net.ParseIP(hostPart); ip != nil && ip.IsLoopback() {
		return nil
	}
	switch hostPart {
	case "", "localhost", "0.0.0.0", "::":
		return nil
	}
	return fmt.Errorf("%w: host %q addr %q is not a loopback or wildcard bind", ErrHostAddr, entry.Name, entry.Addr)
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
