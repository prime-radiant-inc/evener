package mcpconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/toolname"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/execsupport/valueexpr"
)

// ServerConfig describes a single MCP server connection.
type ServerConfig struct {
	Name    string            // from the map key
	Type    string            // "stdio" (default), "sse", "http"
	Command string            // stdio: executable
	Args    []string          // stdio: arguments
	Env     map[string]string // extra env vars (merged with process env)
	URL     string            // sse/http: server URL
	Headers map[string]string // sse/http: extra headers
}

// ServerInfo describes a connected MCP server and its tools.
type ServerInfo struct {
	Name   string   `json:"name"`             // server name as configured
	Tools  []string `json:"tools"`            // namespaced tool names
	Status string   `json:"status,omitempty"` // "connected" / "degraded" / "failed"
	Error  string   `json:"error,omitempty"`  // last failure of any kind, including a Channel-B application error on an otherwise "connected" server; empty when healthy
}

// mcpConfigFile is the JSON structure of an mcp.json file.
type mcpConfigFile struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"` //nolint:tagliatelle // upstream .mcp.json key spelling
}

// mcpServerJSON is the raw JSON form of a single server entry.
type mcpServerJSON struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// LoadFile parses one .mcp.json file and returns server configs
// with env vars expanded.
func LoadFile(path string) ([]ServerConfig, error) {
	return loadFileWithEnv(path, processExpansionEnv())
}

// loadFileWithEnv is LoadFile with the expansion environment supplied by the
// caller.
func loadFileWithEnv(path string, env valueexpr.Env) ([]ServerConfig, error) {
	return loadFile(path, func(s string) (string, error) {
		return expandEnvVarsInEnv(s, env)
	})
}

// loadLayerEvidence reads one discovery layer and returns the evidence for the
// bytes it parsed, so no caller has to read the file again to fingerprint it.
//
// It reads through execenv.ReadFileCapped: the project layer is inside a
// repository the model can write, and a plain os.ReadFile follows whatever the
// path now names. A named pipe there blocks the read forever, and a huge file
// allocates until the process dies. Both are repository-controlled, and both
// would happen on a path a session takes while holding its own lock.
//
// Three outcomes, and callers must keep them apart:
//
//   - parsed: Path is the layer's path and Digest fingerprints its bytes.
//   - absent (os.ErrNotExist): Path is still the layer's path and Digest is "".
//     Absence is a fact worth recording — a file appearing there later is
//     exactly what invalidates it — and collapsing it into "no evidence" would
//     disable the exclusion for every workspace that has no layer yet.
//   - unverifiable: an empty Path. That covers a FIFO, a device, an oversized
//     file, an unreadable entry, or bytes that do not parse, and every caller
//     must treat it as unknown reachability: eligible, never excluded.
func loadLayerEvidence(path string, expand func(string) (string, error)) ([]ServerConfig, LayerEvidence, error) {
	data, err := execenv.ReadFileCapped(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, LayerEvidence{Path: path}, err
		}
		return nil, LayerEvidence{}, err
	}
	configs, err := parseConfigBytes(data, path, expand)
	if err != nil {
		return nil, LayerEvidence{}, err
	}
	return configs, LayerEvidence{Path: path, Digest: ConfigDigest(data), Size: int64(len(data))}, nil
}

func loadFile(path string, expand func(string) (string, error)) ([]ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading MCP config %s: %w", path, err)
	}
	return parseConfigBytes(data, path, expand)
}

// parseConfigBytes is loadFile's parse half, split out so the project layer can
// record evidence for exactly the bytes it parsed.
func parseConfigBytes(data []byte, path string, expand func(string) (string, error)) ([]ServerConfig, error) {
	var cf mcpConfigFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parsing MCP config %s: %w", path, err)
	}

	return parseServerMap(cf.MCPServers, path, expand)
}

// ConfigDigest fingerprints a config layer's bytes. It is the digest
// ProjectConfigEvidence carries, so a later check can tell whether the file
// still says what the classification was derived from.
func ConfigDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ParseServerMap converts a map of server names to raw JSON server entries into
// ServerConfigs, expanding ${VAR} references in each entry. The source string
// is used only for error context (e.g. the file path or "inline").
func ParseServerMap(servers map[string]json.RawMessage, source string) ([]ServerConfig, error) {
	return parseServerMap(servers, source, expandEnvVars)
}

// ParseServerMapUntrusted is ParseServerMap for a config layer evener does not
// author; a $(command) expression is refused rather than run on the host.
func ParseServerMapUntrusted(servers map[string]json.RawMessage, source string) ([]ServerConfig, error) {
	return parseServerMap(servers, source, expandEnvVarsUntrusted)
}

func parseServerMap(servers map[string]json.RawMessage, source string, expand func(string) (string, error)) ([]ServerConfig, error) {
	var configs []ServerConfig
	for name, raw := range servers {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("MCP server name must not be empty in %s", source)
		}
		var sj mcpServerJSON
		if err := json.Unmarshal(raw, &sj); err != nil {
			return nil, fmt.Errorf("parsing MCP server %q in %s: %w", name, source, err)
		}

		cfg, err := serverJSONToConfig(name, sj, expand)
		if err != nil {
			return nil, fmt.Errorf("MCP server %q in %s: %w", name, source, err)
		}
		configs = append(configs, cfg)
	}
	// The servers map ranges in random order; sort by name so the MCP servers
	// come out in the same order on every session build, and the tools they
	// contribute follow that order.
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	return configs, nil
}

func serverJSONToConfig(name string, sj mcpServerJSON, expand func(string) (string, error)) (ServerConfig, error) {
	typ := strings.TrimSpace(sj.Type)
	if typ == "" {
		typ = "stdio"
	}

	command, err := expand(sj.Command)
	if err != nil {
		return ServerConfig{}, fmt.Errorf("expanding command: %w", err)
	}

	args := make([]string, len(sj.Args))
	for i, a := range sj.Args {
		args[i], err = expand(a)
		if err != nil {
			return ServerConfig{}, fmt.Errorf("expanding arg[%d]: %w", i, err)
		}
	}
	if len(sj.Args) == 0 {
		args = nil
	}

	env := make(map[string]string, len(sj.Env))
	for k, v := range sj.Env {
		env[k], err = expand(v)
		if err != nil {
			return ServerConfig{}, fmt.Errorf("expanding env %q: %w", k, err)
		}
	}
	if len(sj.Env) == 0 {
		env = nil
	}

	url, err := expand(sj.URL)
	if err != nil {
		return ServerConfig{}, fmt.Errorf("expanding url: %w", err)
	}

	headers := make(map[string]string, len(sj.Headers))
	for k, v := range sj.Headers {
		headers[k], err = expand(v)
		if err != nil {
			return ServerConfig{}, fmt.Errorf("expanding header %q: %w", k, err)
		}
	}
	if len(sj.Headers) == 0 {
		headers = nil
	}

	return ServerConfig{
		Name:    name,
		Type:    typ,
		Command: command,
		Args:    args,
		Env:     env,
		URL:     url,
		Headers: headers,
	}, nil
}

// processExpansionEnv is the process environment: references and command
// expressions both resolve exactly where the caller runs. Lookup is always
// set because discovery also consults the expansion environment for
// EVENER_NO_USER_SKILLS suppression and global-path resolution.
func processExpansionEnv() valueexpr.Env {
	return valueexpr.Env{Lookup: os.LookupEnv}
}

// launchExpansionEnv is the expansion environment for MCP config layers the
// user authors: the environment a session spawned with the launch layer's
// env overlay would expand them in. The overlay answers variable references
// first (the hub process environment answers after), and command expressions
// run under the parent process environment with the overlay applied last —
// envvars.ApplyOverlay, the one application launchconfig.ToEnv also uses.
// The probe's command environment is the residue a config file can see from
// that: the hub's parent environment plus the user's launch overlay, without
// the per-spawn control variables (EVENER_HUB_TOKEN and siblings) only a
// real child receives. An empty overlay is the process environment, the
// session-side contract.
func launchExpansionEnv(overlay map[string]string) valueexpr.Env {
	if len(overlay) == 0 {
		return processExpansionEnv()
	}
	return valueexpr.Env{
		Lookup: func(name string) (string, bool) {
			if value, ok := overlay[name]; ok {
				return value, true
			}
			return os.LookupEnv(name)
		},
		CommandEnv: envvars.ApplyOverlay(os.Environ(), overlay),
	}
}

// launchOverlayOnlyEnv is the expansion environment for config layers the
// probing process does not author — the project's .evener/mcp.json. The
// settings probe runs with no session launch and no project trust decision
// behind it, and the probing process's environment holds the operator's own
// credentials, so only the launch configuration's env overlay — the values
// the user explicitly configured for this project — may answer a reference
// in repo-controlled content. An empty overlay answers nothing, so an
// unanswerable reference surfaces as the standard "variable not set"
// expansion error and the layer is skipped with a warning.
func launchOverlayOnlyEnv(overlay map[string]string) valueexpr.Env {
	return valueexpr.Env{
		Lookup: func(name string) (string, bool) {
			value, ok := overlay[name]
			return value, ok
		},
	}
}

// expandEnvVars expands a config value's $ expressions through the shared
// parser: $NAME, ${NAME}, ${NAME:-default}, $$, and $(command), whose
// whitespace-trimmed stdout is the value. MCP config now shares the union
// grammar (spec §10) with providers.toml: a bare $NAME and an unterminated ${
// were literal text here before and are expressions now, and a :- default
// fills an unset or empty variable, where it filled only an unset one.
//
// Any unresolved expression is an error, matching how MCP config treats a
// missing variable today; the caller names the field it was expanding.
func expandEnvVars(s string) (string, error) {
	return expandEnvVarsInEnv(s, processExpansionEnv())
}

// expandEnvVarsInEnv is expandEnvVars under one expansion environment: a
// session expands through its own process env, the settings probe through
// the launch layer's overlay.
func expandEnvVarsInEnv(s string, env valueexpr.Env) (string, error) {
	expanded, unresolved, err := valueexpr.ExpandIn(s, env)
	if err != nil {
		return "", fmt.Errorf("invalid $ expression: %w", err)
	}
	if len(unresolved) > 0 {
		// The first unresolved expression names the failure; Expand reports
		// every one, but one reason is all an author needs to fix the value.
		u := unresolved[0]
		if u.Command != "" {
			return "", fmt.Errorf("command expression failed: %w", u.Err)
		}
		return "", fmt.Errorf("environment variable %q is not set (use ${%s:-default} to provide a default)", u.Name, u.Name)
	}
	return expanded, nil
}

// expandEnvVarsUntrusted is expandEnvVars for a config layer evener does not
// author: the project's .evener/mcp.json, which the model can write
// mid-session, and plugins' configs, which are third-party content. Expansion
// runs commands on the host, so a $(command) expression there is refused at
// load rather than executed.
func expandEnvVarsUntrusted(s string) (string, error) {
	return expandEnvVarsUntrustedInEnv(s, processExpansionEnv())
}

// expandEnvVarsUntrustedInEnv is expandEnvVarsUntrusted under one expansion
// environment.
func expandEnvVarsUntrustedInEnv(s string, env valueexpr.Env) (string, error) {
	// The refusal keys on the command pieces alone, never on a clean scan:
	// Scan reports what it found even when a later syntax error aborts it,
	// and Expand executes command pieces as it walks, so a value like
	// "$(evil) ${unterminated" would run evil before its own error surfaces.
	if scan, _ := valueexpr.Scan(s); len(scan.Commands) > 0 {
		return "", errors.New("command expressions $(...) are allowed only in config you author yourself (the global mcp.json or --mcp-config files)")
	}
	return expandEnvVarsInEnv(s, env)
}

// ParseInline parses a "name:command args..." inline spec into an ServerConfig.
func ParseInline(spec string) (ServerConfig, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ServerConfig{}, errors.New("empty MCP inline spec")
	}

	before, after, ok := strings.Cut(spec, ":")
	if !ok {
		return ServerConfig{}, fmt.Errorf("MCP inline spec missing colon: %q (format: name:command args...)", spec)
	}

	name := strings.TrimSpace(before)
	rest := strings.TrimSpace(after)
	if name == "" {
		return ServerConfig{}, fmt.Errorf("MCP inline spec has empty name: %q", spec)
	}
	if rest == "" {
		return ServerConfig{}, fmt.Errorf("MCP inline spec has empty command: %q", spec)
	}

	parts := strings.Fields(rest)
	var args []string
	if len(parts) > 1 {
		args = parts[1:]
	}

	return ServerConfig{
		Name:    name,
		Type:    "stdio",
		Command: parts[0],
		Args:    args,
	}, nil
}

// Merge merges multiple layers of configs. Later layers shadow
// earlier layers by server name.
func Merge(layers ...[]ServerConfig) []ServerConfig {
	seen := map[string]int{} // name -> index in result
	var result []ServerConfig

	for _, layer := range layers {
		for _, cfg := range layer {
			if idx, ok := seen[cfg.Name]; ok {
				result[idx] = cfg
			} else {
				seen[cfg.Name] = len(result)
				result = append(result, cfg)
			}
		}
	}
	return result
}

// Discover loads MCP configs from all sources:
// global (~/.config/evener/mcp.json) -> project (.evener/mcp.json at git root)
// -> CLI files -> CLI inline specs. Later sources shadow earlier by name.
// Layer 1 (global) is omitted entirely when EVENER_NO_USER_SKILLS=1 (#3487).
//
// A global or project config file (layers 1-2) that fails to parse or expand
// is not fatal: its layer is skipped and a warning naming the file and error
// is returned alongside the configs actually loaded. A missing global/project
// file is still silent, not a warning. CLI-supplied files/specs (layers 3-4)
// are explicit user input for this invocation, so an error there stays fatal.
func Discover(env execenv.ExecutionEnvironment, extraFiles, inlineSpecs []string) ([]ServerConfig, []string, error) {
	// A session runs inside its own launched process, so the inherited
	// process environment is the full environment for every layer, trusted
	// or not — the trust decision happened when the user launched there.
	env0 := processExpansionEnv()
	configs, _, _, warnings, err := discoverLayers(env, extraFiles, inlineSpecs, env0, env0)
	return configs, warnings, err
}

// ProjectConfigPath is the layer-2 filename a session's project MCP config
// lives at: <git root>/.evener/mcp.json, or "" when the environment has no git
// root. It is the one derivation of that path; discovery and any caller that
// needs to tell whether that layer still describes the workspace read it here,
// so the two cannot disagree about which file they mean.
func ProjectConfigPath(env execenv.ExecutionEnvironment) string {
	if env == nil {
		return ""
	}
	// Resolved WITHOUT the per-environment memo, deliberately.
	//
	// Discovery answers "which project config would this process read", and a
	// delegate construction asks it of the environment it was handed — often the
	// parent's shared one. The memo on that environment was filled when the
	// parent started, so a session launched before `git init` would read the
	// remembered "no repository" for the rest of its life: the parent's
	// freshness check notices the new root and makes the tool eligible, while
	// the child discovers nothing there and cannot register it. The roster then
	// advertises a tool the child does not have, which is the same
	// disagreement this package exists to keep out.
	//
	// Freshness already resolves this way, so both sides of the comparison ask
	// the workspace the same question. The cost is one git fork per discovery,
	// and discovery runs at session init.
	path, _ := ProjectConfigPathFreshDefinitive(env)
	return path
}

// ProjectConfigPathFreshDefinitive is the one resolution of this layer's path,
// keeping whether the
// workspace's Git root was CONFIRMED. An empty path with a false flag means the
// root could not be determined, which is not the same fact as a confirmed
// absence of a repository, and a caller comparing evidence must not confuse
// them: the first leaves the question open, the second answers it.
func ProjectConfigPathFreshDefinitive(env execenv.ExecutionEnvironment) (string, bool) {
	if env == nil {
		return "", false
	}
	root, definitive := execenv.GitRootOrEmptyFreshDefinitive(env, env.WorkingDirectory())
	if root == "" {
		// No root: the path this layer would occupy is unresolved, and whether
		// that is a fact about the workspace or a failure to read it is exactly
		// what the flag carries.
		return "", definitive
	}
	return filepath.Join(root, ".evener", "mcp.json"), true
}

// LayerEvidence is one configuration layer as discovery read it: the path the
// layer resolved to and a digest of the exact bytes that were parsed. Digest is
// empty when the layer was simply absent, and Path is empty when the layer could
// not be verified at all.
//
// It exists because a classification derived from the parsed layer and a
// fingerprint taken by a second read are two facts that can disagree — a file
// edited between them pairs a stale classification with a fresh digest, and the
// freshness check then accepts a classification that no longer describes the
// file. Discovery returns this alongside the layer precisely so no caller has
// to take that second read.
type LayerEvidence struct {
	Path   string
	Digest string
	// Size is the byte length of the file Digest fingerprints. It is recorded so
	// a freshness check can refuse a file whose size already differs without
	// reading a byte, and so a layer recorded ABSENT (empty Digest) can be
	// checked by existence alone. Without it, every check had to read the whole
	// file to discover that it had nothing to compare against.
	Size int64
}

// ConfigEvidence is the evidence for every layer that can make a CLI-only
// server's tools reachable again, read in the same pass that parsed each one.
//
// The set is what it is because the exclusion it guards is only sound while
// EVERY layer the child would discover still says what the classification was
// derived from. A matching server added to the global layer, a project layer
// that appeared or changed, and a git root that moved are each on their own
// enough to make a previously-unreachable name reachable, so each is recorded.
type ConfigEvidence struct {
	Global  LayerEvidence
	Project LayerEvidence
}

// EvidenceCurrent reports whether every layer still says what it said when
// discovery read it, so a classification derived from those layers still
// describes what a child would discover now.
//
// The paths are resolved FRESH on every call, which is the point: a session that
// starts outside Git and later has a repository initialized around it — or one
// that switches branches in place — keeps its environment, so no swap runs and
// nothing else would notice that the layers moved. A memoized root would answer
// the question it is being asked to re-answer.
//
// Anything it cannot establish answers false, which callers must treat as
// unknown reachability: eligible, never excluded. It reads at most two files and
// connects nothing, so it cannot become the probe an earlier round ruled out.
func (e ConfigEvidence) EvidenceCurrent(env execenv.ExecutionEnvironment) bool {
	if env == nil {
		return false
	}
	if !layerEvidenceCurrent(e.Global, globalMCPConfigPathForFreshness(processExpansionEnv().Lookup)) {
		return false
	}
	path, definitive := ProjectConfigPathFreshDefinitive(env)
	if path == "" && !definitive {
		// The workspace's Git root could not be determined, so this check cannot
		// say whether the project layer moved. It answers "not current", which
		// every caller reads as unknown reachability and therefore leaves the
		// names ELIGIBLE.
		//
		// That direction is the whole point. The comparison below would see an
		// empty resolved path, match it against an empty recorded path, and call
		// the evidence current — so a session that started outside Git and whose
		// fresh root resolution then times out would keep excluding a tool that a
		// worktree, appearing around the session with a matching MCP fallback,
		// makes reachable again. "Could not read the root" is not "there is no
		// repository": the absence was never established, so it must not be
		// recorded as one, and the ceiling must let the child try.
		return false
	}
	return layerEvidenceCurrent(e.Project, path)
}

// layerEvidenceCurrent compares one layer's recorded evidence against what that
// path says now. An empty recorded Path is a layer discovery never resolved, and
// it stays current only while the workspace still resolves to none.
//
// The file's identity is settled by STAT before anything is read, because this
// runs on every model round:
//
//   - Recorded absent (empty Digest): the layer is current only while the path
//     still does not exist. A file that appeared there is a change, and it is
//     answered by the stat that found it — reading it would allocate its whole
//     contents on every request just to learn the recorded digest is empty.
//   - Recorded present: a size that differs from the recorded one is already a
//     change, so it answers without a read. Only a same-size file is read, where
//     the digest comparison is the thing that can still tell them apart.
//
// A path that cannot be statted is UNKNOWN, not absent: the caller reads "not
// current" as unknown reachability and leaves the names eligible.
func layerEvidenceCurrent(recorded LayerEvidence, path string) bool {
	if recorded.Path == "" {
		return path == ""
	}
	if path != recorded.Path {
		return false
	}
	// os.Stat, not Lstat: DISCOVERY follows the path, so a dangling symlink is
	// recorded absent here exactly as the read that parsed the layer saw it. Lstat
	// finds the link itself and would call such a layer present, marking unchanged
	// evidence stale forever and leaving CLI-only tools advertised while the child
	// discovers no fallback. A link whose target appears is then a change, and the
	// recorded-absent branch answers it by existence alone.
	info, err := os.Stat(recorded.Path)
	if err != nil {
		// Absent is a successful stat of nothing, which is exactly what a layer
		// recorded absent continues to be. Any other failure is unknown.
		return errors.Is(err, os.ErrNotExist) && recorded.Digest == ""
	}
	if recorded.Digest == "" {
		// Recorded absent, yet the path resolves now: the layer changed.
		return false
	}
	if info.Mode().IsRegular() && info.Size() != recorded.Size {
		return false
	}
	data, err := execenv.ReadFileCapped(recorded.Path)
	if err != nil {
		return false
	}
	return int64(len(data)) == recorded.Size && ConfigDigest(data) == recorded.Digest
}

// DiscoverWithCLIOnlyAndEvidence is Discover plus two things read in the same
// pass: the servers the CLI layers (--mcp-config files and --mcp inline specs)
// contribute to the EFFECTIVE configuration, and every layer's own evidence.
//
// A caller that clears the two CLI layers — every delegate spawn does — uses the
// CLI entries to decide which tool names a child might not reach, and reading
// that answer from this one parse keeps it from drifting with a second one. The
// returned configs are Discover's, unchanged.
//
// Which CLI entries it returns, and what the caller may conclude from each:
//
//   - An entry whose name no other layer declares: Fallback is nil. The child
//     clears the CLI layers and discovers nothing by that name, so the server,
//     and only it, supplies those tools.
//   - An entry whose name another layer declares: Fallback is that layer's
//     config, the one the child discovers instead. This entry REPLACES the
//     other layer's entry for this session, so its names are exactly the names
//     THIS configuration serves; the two configurations may expose different
//     tool sets under the same server name.
//
// Both are returned, and neither is a reachability verdict. A shadowed entry's
// tools may or may not be reachable from a child, and the answer is not a
// prediction here: the child's own MCP initialization registers what that
// configuration exposes when it spawns. Callers must therefore treat an
// unknown reachability as "let the child try" — excluding a name on that
// ignorance hides a tool the child can call.
//
// Plugin MCP configs are NOT a layer here: plugin discovery is the session's,
// not this package's, and initMCP merges them under these configs. A caller
// that knows its plugin server names must treat a CLI entry whose name
// collides with one as shadowed, for the same reason and with no subtraction
// of tools: the child still loads the plugin, so it rediscovers that server
// (see recordCLIMCPServers).
func DiscoverWithCLIOnlyAndEvidence(env execenv.ExecutionEnvironment, extraFiles, inlineSpecs []string) ([]ServerConfig, []CLIOnlyServer, ConfigEvidence, []string, error) {
	env0 := processExpansionEnv()
	return discoverLayers(env, extraFiles, inlineSpecs, env0, env0)
}

// DiscoverWithLaunchEnv is Discover with the launch layer's environment
// overlay applied: the values a session spawned under that launch layer
// receives on top of the process environment (launchconfig's env field,
// which ToEnv applies last so it wins). Layers the user authors — the
// global config, launch/CLI files, inline specs — resolve every
// environment decision the way that session's process would: variable
// references and $(command) expressions, EVENER_NO_USER_SKILLS suppression,
// and XDG_CONFIG_HOME/HOME resolution, overlay first, process environment
// after, never mutating the caller's own environment.
//
// Layers the user does not author — the project's .evener/mcp.json — see
// only the overlay. The caller is a probe (the hub's settings overview)
// that runs with no session launch and no project trust decision behind it,
// and the probing process's environment holds the operator's own
// credentials: a repo-controlled file that could read them would hand them
// to any endpoint it names just by rendering the overview. The overlay is
// the explicit authorization — values the user configured for this project
// answer; a reference it does not answer fails expansion and skips the
// layer with a warning, the same visible skip as any other project-layer
// parse failure. A session's own Discover, inside the launched process,
// keeps the full inherited environment for the project layer. Inline specs
// are not expanded today, matching Discover.
func DiscoverWithLaunchEnv(env execenv.ExecutionEnvironment, extraFiles, inlineSpecs []string, launchEnv map[string]string) ([]ServerConfig, []string, error) {
	configs, _, _, warnings, err := discoverLayers(env, extraFiles, inlineSpecs, launchExpansionEnv(launchEnv), launchOverlayOnlyEnv(launchEnv))
	return configs, warnings, err
}

// discoverLayers is Discover's body with the expansion environment chosen
// per layer trust class: trustedEnv answers the layers the user authors —
// the global config and the launch/CLI files — and untrustedEnv answers
// the project's .evener/mcp.json, which evener does not author. The two
// agree inside a session (Discover passes the full process environment for
// both); they differ for the settings probe (DiscoverWithLaunchEnv), which
// restricts the project layer to the launch overlay.
func discoverLayers(env execenv.ExecutionEnvironment, extraFiles, inlineSpecs []string, trustedEnv, untrustedEnv valueexpr.Env) ([]ServerConfig, []CLIOnlyServer, ConfigEvidence, []string, error) {
	var layers [][]ServerConfig
	var warnings []string
	var evidence ConfigEvidence

	// Layer 1: Global config. The tool-fluency harness sets EVENER_NO_USER_SKILLS
	// so an eval round depends only on the revision under test; the operator's
	// global MCP servers are one source that must not reach it (#3487). The
	// project, CLI-file, and CLI-inline layers below stay, so a probe can still
	// exercise MCP that its own fixture declares. The check runs through the
	// expansion environment so a launch layer can suppress the same layer for
	// the sessions it spawns.
	if value, _ := trustedEnv.Lookup(envvars.EVENERNoUserSkills.Name); value != "1" {
		globalPath := globalMCPConfigPathInEnv(trustedEnv.Lookup)
		if globalPath != "" {
			configs, layerEvidence, err := loadLayerEvidence(globalPath, func(s string) (string, error) {
				return expandEnvVarsInEnv(s, trustedEnv)
			})
			evidence.Global = layerEvidence
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					warnings = append(warnings, fmt.Sprintf("MCP config %s: %v", globalPath, err))
				}
				// Missing global file is not an error.
			} else {
				layers = append(layers, configs)
			}
		}
	}

	// Layer 2: Per-project config (.evener/mcp.json at git root), loaded
	// untrusted: that layer is model-writable, and expansion runs commands on
	// the host, so its $(command) expressions are refused at load — the same
	// skip-with-warning as any other project-layer parse failure.
	if env != nil {
		if projPath := ProjectConfigPath(env); projPath != "" {
			configs, layerEvidence, err := loadLayerEvidence(projPath, func(s string) (string, error) {
				return expandEnvVarsUntrustedInEnv(s, untrustedEnv)
			})
			evidence.Project = layerEvidence
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					warnings = append(warnings, fmt.Sprintf("MCP config %s: %v", projPath, err))
				}
			} else {
				layers = append(layers, configs)
			}
		}
	}

	// Layers 3-4: CLI config files, then CLI inline specs.
	var cliLayer []ServerConfig
	for _, path := range extraFiles {
		configs, err := loadFileWithEnv(path, trustedEnv)
		if err != nil {
			return nil, nil, ConfigEvidence{}, nil, fmt.Errorf("--mcp-config %s: %w", path, err)
		}
		cliLayer = append(cliLayer, configs...)
	}
	for _, spec := range inlineSpecs {
		cfg, err := ParseInline(spec)
		if err != nil {
			return nil, nil, ConfigEvidence{}, nil, fmt.Errorf("--mcp %q: %w", spec, err)
		}
		cliLayer = append(cliLayer, cfg)
	}
	// The layers the child still discovers when it clears the two CLI ones.
	// Captured before the CLI layer is appended so cliOnly compares against
	// exactly those, rather than slicing the merged list back apart.
	shared := append([][]ServerConfig(nil), layers...)
	if len(cliLayer) > 0 {
		layers = append(layers, cliLayer)
	}

	return Merge(layers...), cliOnly(cliLayer, shared), evidence, warnings, nil
}

// CLIOnlyServer is a CLI-layer server plus, when another layer declares a server
// of the same NAME NAMESPACE, the config that layer would contribute. Fallback
// is nil when the CLI layers are the only declarers: the child clears them and
// discovers nothing by that name. When Fallback is set, the CLI entry shadows
// that config for this session and the child, having cleared the CLI layers,
// discovers the fallback instead.
//
// The match is on the namespace MCP registration derives, not on the raw
// configured string: registration namespaces every tool as
// "<server>__<tool>" and replaces hyphens with underscores, so a project server
// "team-tools" and a CLI server "team_tools" register into one namespace and
// serve the same tool names. Comparing raw names would report the CLI entry as
// unshadowed, and a caller that then excluded its names would hide a tool the
// child registers through the project entry.
type CLIOnlyServer struct {
	Config   ServerConfig
	Fallback *ServerConfig
}

// cliOnly reports the CLI-layer servers, each with the config another layer
// declares in the same normalized name namespace (nil when no other layer
// does). Later layers win the fallback, matching Merge's precedence.
func cliOnly(cliLayer []ServerConfig, shared [][]ServerConfig) []CLIOnlyServer {
	if len(cliLayer) == 0 {
		return nil
	}
	elsewhere := make(map[string]ServerConfig, len(shared))
	for _, layer := range shared {
		for _, cfg := range layer {
			// Later layers shadow earlier ones, matching Merge.
			elsewhere[toolname.SanitizeMCPToolName(cfg.Name)] = cfg
		}
	}
	var out []CLIOnlyServer
	for _, cfg := range cliLayer {
		if fallback, ok := elsewhere[toolname.SanitizeMCPToolName(cfg.Name)]; ok {
			out = append(out, CLIOnlyServer{Config: cfg, Fallback: &fallback})
			continue
		}
		out = append(out, CLIOnlyServer{Config: cfg})
	}
	return out
}

// DiscoverTrusted is Discover restricted to the layers the session's MODEL cannot
// write: the global config (~/.config/evener/mcp.json; omitted entirely when
// EVENER_NO_USER_SKILLS=1, #3487), --mcp-config files, and --mcp inline specs.
// The per-project layer (<git root>/.evener/mcp.json) is deliberately EXCLUDED
// because it lives inside the model's own write surface.
//
// Use it wherever MCP config feeds a SECURITY decision rather than a connection.
// The sandbox policy derivation (agent.SessionInfraRoots) is the case that
// matters: sandbox grants are resolved from it, and SandboxPolicy promises that
// nothing the model does mid-session can widen its own box. A model that could
// plant .evener/mcp.json and have it grant filesystem roots would break exactly
// that promise. Ordinary MCP startup still uses Discover — connecting to a
// project-declared server is the feature; granting it sandbox roots is not.
func DiscoverTrusted(extraFiles, inlineSpecs []string) ([]ServerConfig, []string, error) {
	// A nil env is what suppresses the project layer; see Discover's layer 2.
	return Discover(nil, extraFiles, inlineSpecs)
}

// globalMCPConfigPathInEnv resolves the global config path the way the
// expansion environment's session would: XDG_CONFIG_HOME and the home
// directory come through lookup (a launch layer's overlay answers first, the
// process environment after), with os.UserHomeDir's own GOOS home spellings —
// so an overlay redirects the probe's global layer exactly where it
// redirects a spawned session's.
func globalMCPConfigPathInEnv(lookup func(string) (string, bool)) string {
	xdg, _ := lookup(envvars.XDGConfigHome.Name)
	home := func() (string, error) {
		if home := userdirs.HomeFromLookup(runtime.GOOS, lookup); home != "" {
			return home, nil
		}
		return "", errors.New("home directory unresolvable from the expansion environment")
	}
	return globalMCPConfigPathWithHome(xdg, home)
}

// globalMCPConfigPathWithHome is the path primitive: an XDG base (empty
// falls back to home) and the home lookup whose failure yields no path.
func globalMCPConfigPathWithHome(xdgConfigHome string, userHomeDir func() (string, error)) string {
	return userdirs.Subdir(userdirs.ConfigRoot(xdgConfigHome, userHomeDir), "mcp.json")
}

// globalMCPConfigPathForFreshness resolves the global layer the way DISCOVERY
// does, suppression included, so the freshness check and the classification it
// guards cannot disagree about whether that layer exists.
//
// The rule: discovery omits the global layer entirely under
// EVENER_NO_USER_SKILLS=1 (#3487), in which case it records no path at all. A
// freshness check that resolved the path anyway would compare a recorded ""
// against a real path, answer "not current" on every single check, and disable
// the CLI-only exclusion for the whole session. An operator opting out of user
// skills would silently lose a capability the ceiling still has to bound.
//
// Resolving the same path under the same suppression keeps the two in step: the
// layer is suppressed for both, so the recorded empty path stays current for
// both.
func globalMCPConfigPathForFreshness(lookup func(string) (string, bool)) string {
	if value, _ := lookup(envvars.EVENERNoUserSkills.Name); value == "1" {
		return ""
	}
	return globalMCPConfigPathInEnv(lookup)
}
