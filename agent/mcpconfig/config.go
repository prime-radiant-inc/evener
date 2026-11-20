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
	// DenyNetworkException withholds the trusted-infrastructure network
	// carve-out from this server. It is set on a project-layer stdio entry that
	// a sandboxed delegate admitted WITHOUT launch authorization, so the spawn
	// runs under ordinary sandbox confinement: net=off severs its egress
	// exactly as it severs a model-authored spawn, while local tools still
	// work. The zero value keeps the existing exception for every global, CLI,
	// plugin, and launch-authorized server, which is why the field is a
	// refusal and not an allowance.
	//
	// It is discovery metadata for the spawn site, never configuration: the
	// JSON tag keeps it out of any encoding of a server, and no config layer
	// can set it.
	DenyNetworkException bool `json:"-"`
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

// readConfigFile is execenv.ReadFileCapped behind a package variable, so a test
// can count how many times discovery reads a layer and can serve different bytes
// to a second read. The single-read property is the security claim these tests
// exist to hold; it cannot be observed through the exported API otherwise.
var readConfigFile = execenv.ReadFileCapped

// layerRead is one config layer read and parsed exactly ONCE: the servers that
// came out of it, the evidence for the same bytes, the per-server warnings, and
// which servers the launch authorized.
//
// The single read is the security property, not an optimization. A caller that
// hashed one read and then re-read the file to authorize it holds two facts that
// can disagree: a file edited in between pairs a stale authorization with fresh
// bytes, and the server the authorization was meant to withhold is the one that
// gets expanded through the process environment. Every field here derives from
// one []byte slice.
type layerRead struct {
	Configs  []ServerConfig
	Evidence LayerEvidence
	// Warnings are per-server refusals: an entry that would not parse, or one
	// the launch did not authorize and the restricted environment could not
	// expand. They are warnings and not a layer error so the entries beside
	// them still register.
	Warnings []string
	// Authorized names the servers the launch authorized, by configured name.
	// Nil when the layer was parsed without per-server authorization.
	Authorized map[string]bool
}

// layerReadOptions is one layer read's inputs.
type layerReadOptions struct {
	// expand answers every entry, and is used only when authorize is nil.
	expand func(string) (string, error)
	// authorize decides per entry: which expander parses it, and whether the
	// launch authorized it. Nil parses the layer as one trust class, which is
	// every layer but an eligible-or-not project layer under a sandboxed
	// delegate.
	authorize layerServerAuthorization
	// recordServerDigests records a digest per raw entry on the returned
	// evidence. The PROJECT layer always sets it, because those digests are
	// what a later sandboxed delegate matches its own read against; the global
	// and CLI layers, which no one matches per entry, leave it off.
	recordServerDigests bool
}

// layerServerAuthorization is the per-server decision for one raw entry in a
// project config layer: the expander to parse that server with, and whether the
// resulting server is launch-authorized.
//
// It exists because a project layer is not one trust class per file. A server
// the launch already read is authorized; a server added to the same file since
// is not, and the difference is per ENTRY. Collapsing the two would either
// discard the authorized server or expand the new one through the process
// environment, and the second is the disclosure the whole model exists to stop.
type layerServerAuthorization func(name string, raw json.RawMessage) (expand func(string) (string, error), authorized bool)

// projectServerDigests fingerprints every raw server entry in one config layer,
// keyed by server name. The digest is over the entry's own bytes as they appear
// in the file, so it identifies one server entry and not the file that holds it:
// adding, removing, or editing a DIFFERENT server leaves this one's digest
// alone, which is what makes per-server authorization possible at all.
func projectServerDigests(servers map[string]json.RawMessage) map[string]string {
	if len(servers) == 0 {
		return nil
	}
	digests := make(map[string]string, len(servers))
	for name, raw := range servers {
		digests[name] = ConfigDigest(raw)
	}
	return digests
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
//     file, or an unreadable entry, and every caller must treat it as unknown
//     reachability: eligible, never excluded.
//
// A layer whose bytes read but do not parse keeps its Path and Digest: the
// bytes were read, and a later freshness check comparing them is answering a
// question about the same read.
func loadLayerEvidence(path string, expand func(string) (string, error)) ([]ServerConfig, LayerEvidence, error) {
	read, err := readLayer(path, layerReadOptions{expand: expand})
	return read.Configs, read.Evidence, err
}

// readLayer is the one read of a config layer. It parses the layer as a single
// trust class when opts.authorize is nil, and entry by entry when it is not.
//
// An entry that fails to parse or expand under a per-server authorization is
// recorded in Warnings naming that server, and the rest of the layer still
// loads. That is the difference between a layer and a server: one
// `$(command)` or one unresolvable ${OTHER_SECRET} must not take the unchanged
// authorized entries beside it down with it, and before this split it did.
func readLayer(path string, opts layerReadOptions) (layerRead, error) {
	data, err := readConfigFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return layerRead{Evidence: LayerEvidence{Path: path}}, err
		}
		return layerRead{}, err
	}
	evidence := LayerEvidence{Path: path, Digest: ConfigDigest(data), Size: int64(len(data))}

	var cf mcpConfigFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return layerRead{Evidence: evidence}, fmt.Errorf("parsing MCP config %s: %w", path, err)
	}
	if opts.recordServerDigests {
		evidence.ServerDigests = projectServerDigests(cf.MCPServers)
	}
	if opts.authorize == nil {
		configs, err := parseServerMap(cf.MCPServers, path, opts.expand)
		return layerRead{Configs: configs, Evidence: evidence}, err
	}
	configs, authorized, warnings := parseServerMapAuthorized(cf.MCPServers, path, opts.authorize)
	return layerRead{Configs: configs, Evidence: evidence, Warnings: warnings, Authorized: authorized}, nil
}

// parseServerMapAuthorized converts a server map entry by entry, keeping every
// entry that parses and warning about each one that does not. Ordering matches
// parseServerMap, and an empty name is a warning rather than a layer refusal for
// the same reason: it disables the one entry, not the file.
func parseServerMapAuthorized(servers map[string]json.RawMessage, source string, authorize layerServerAuthorization) ([]ServerConfig, map[string]bool, []string) {
	configs := make([]ServerConfig, 0, len(servers))
	authorized := make(map[string]bool, len(servers))
	var warnings []string
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw := servers[name]
		if strings.TrimSpace(name) == "" {
			warnings = append(warnings, fmt.Sprintf("MCP config %s: MCP server name must not be empty", source))
			continue
		}
		expand, isAuthorized := authorize(name, raw)
		var sj mcpServerJSON
		if err := json.Unmarshal(raw, &sj); err != nil {
			warnings = append(warnings, fmt.Sprintf("MCP config %s: parsing MCP server %q: %v", source, name, err))
			continue
		}
		cfg, err := serverJSONToConfig(name, sj, expand)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("MCP config %s: MCP server %q: %v", source, name, err))
			continue
		}
		if isAuthorized {
			authorized[name] = true
		}
		configs = append(configs, cfg)
	}
	return configs, authorized, warnings
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
	configs, _, _, warnings, err := discoverLayers(discoverOptions{
		env:          env,
		extraFiles:   extraFiles,
		inlineSpecs:  inlineSpecs,
		trustedEnv:   env0,
		untrustedEnv: env0,
	})
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

// projectRepoRoot is the canonical main repository root the workspace belongs
// to, or "" when the workspace is not inside a resolvable repository.
//
// It is the identity two checkouts of ONE repository share: the main checkout
// and every linked worktree of it resolve here, while an unrelated repository
// resolves somewhere else. Discovery records it on the project layer's evidence
// so an authorization check can tell "the same committed file, read in a
// worktree" from "the same bytes, planted in a different repository".
//
// Delegates to execenv.ResolveMainRepoRoot (memoized per environment).
func projectRepoRoot(env execenv.ExecutionEnvironment) string {
	if env == nil {
		return ""
	}
	return execenv.ResolveMainRepoRoot(env, env.WorkingDirectory())
}

// LayerEvidence is one configuration layer as discovery read it: the path the
// layer resolved to and a digest of the exact bytes that were parsed. Digest is
// empty when the layer was simply absent, and Path is empty when the layer could
// not be verified at all.
//
// Path is ALSO empty for a layer whose bytes read but did not PARSE. Dropping
// such a layer here is not the same as establishing its absence: a sandboxed
// child parses the file entry by entry and registers the entries beside the
// broken one, so a classification derived from the dropped layer understates
// what the child reaches. Empty Path is the established "unknown reachability"
// answer — EvidenceCurrent returns false for it — and every reader then leaves
// the affected names eligible. The digest stays recorded, because it was taken
// from the same read and remains a true statement about those bytes.
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
	// RepoRoot is the canonical main repository root the layer's workspace
	// belonged to when discovery read it: the shared checkout that a
	// repository's own working tree and each of its linked worktrees resolve
	// to. It is what lets a sandboxed delegate launched into a worktree of the
	// SAME repository recognize the committed copy of the bytes the startup
	// read parsed (see projectLayerEligible). Empty when the
	// workspace was not a resolvable repository, and no match may treat two
	// empty identities as the same repository.
	RepoRoot string
	// ServerDigests fingerprints each raw mcpServers entry in the layer, keyed
	// by server name, over the entry's own JSON bytes from the read that
	// produced Digest.
	//
	// It is what makes launch authorization PER SERVER. Digest answers "is this
	// still the same file"; ServerDigests answers "is this still the same
	// server", so a project layer that gained one entry since the launch still
	// authorizes the entries it already had. Without it the only choices were
	// authorizing a whole modified file or discarding every server in it.
	//
	// Nil for a layer that recorded no server entries, and for every layer
	// whose servers are not launch-authorizable per entry (global, CLI).
	ServerDigests map[string]string
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
// A path other than the recorded one is never current: that is a different
// layer, whatever its bytes say. The one caller that deliberately carries a
// layer's bytes to another path — a delegate's worktree — asks layerBytesCurrent
// itself, after proving the two paths are in the same repository.
func layerEvidenceCurrent(recorded LayerEvidence, path string) bool {
	if recorded.Path == "" {
		return path == ""
	}
	if path != recorded.Path {
		return false
	}
	return layerBytesCurrent(recorded, path)
}

// layerBytesCurrent compares one layer's recorded bytes against the file at
// path, wherever that path is. It is layerEvidenceCurrent without the
// same-path requirement, for a caller that has already established the path is
// a different working tree of the same repository.
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
func layerBytesCurrent(recorded LayerEvidence, path string) bool {
	// os.Stat, not Lstat: DISCOVERY follows the path, so a dangling symlink is
	// recorded absent here exactly as the read that parsed the layer saw it. Lstat
	// finds the link itself and would call such a layer present, marking unchanged
	// evidence stale forever and leaving CLI-only tools advertised while the child
	// discovers no fallback. A link whose target appears is then a change, and the
	// recorded-absent branch answers it by existence alone.
	info, err := os.Stat(path)
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
	data, err := execenv.ReadFileCapped(path)
	if err != nil {
		return false
	}
	return int64(len(data)) == recorded.Size && ConfigDigest(data) == recorded.Digest
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
	configs, _, _, warnings, err := discoverLayers(discoverOptions{
		env:          env,
		extraFiles:   extraFiles,
		inlineSpecs:  inlineSpecs,
		trustedEnv:   launchExpansionEnv(launchEnv),
		untrustedEnv: launchOverlayOnlyEnv(launchEnv),
	})
	return configs, warnings, err
}

// DiscoverWithCLIOnlyAndEvidenceForProjectEnv discovers MCP configs along with
// CLI-only server metadata and layer evidence, applying per-server launch
// authorization for a sandboxed delegate's project layer.
//
// A normal launch answers its project layer with the full process environment,
// because the user launched into that workspace and that launch is the trust
// decision (see Discover). A SANDBOXED DELEGATE has no such decision behind it.
// It is constructed by a running session, in a shared workspace, possibly after
// a Git root and its .evener/mcp.json appeared around that session. The sandbox
// withholds host credentials from the child, yet discovery would expand the
// repository's ${VAR} references out of os.Environ() and send the results to
// the endpoint the repository names, before the child's tool ceiling has
// anything to say about it.
//
// Authorization is PER SERVER, and it is decided from the SAME bytes that are
// then parsed (one read; see readLayer). A project server
// entry is launch-authorized when both hold:
//
//   - The layer is eligible: the workspace's project layer is the one the
//     launch read (same path), or the same committed bytes at the path of a
//     linked worktree of the SAME repository (see projectLayerEligible).
//   - The entry is unchanged: its raw bytes digest matches the startup
//     record's digest for that server name.
//
// An authorized entry expands through the process environment, exactly as a
// session's own Discover would. Every other entry — added since the launch, or
// in a layer the launch never read — expands through launchOverlayOnlyEnv(nil),
// where every reference is unanswered, so a reference fails and that ONE server
// is skipped with a warning naming it. Literal values expand to themselves and
// still load. The unchanged authorized servers in the same file keep working,
// which is the difference between withholding a credential and disabling the
// repository's whole MCP config.
//
// The network exception follows authorization too. A sandboxed session's stdio
// MCP servers run under ConfineTrustedInfra, which deliberately keeps the
// network reachable even at net=off (see agent/internal/mcp), and sse/http
// servers are dialled by the host process itself — so an unauthorized project
// entry would reach the host network no matter what the sandbox says. A stdio
// entry is therefore still ADMITTED, with DenyNetworkException set, so the spawn
// site confines it ordinarily and net=off severs its egress while its local
// tools keep working. A network-transport entry cannot be saved that way, and
// with noNet it is skipped with a warning instead of connected.
// Launch-authorized servers keep the exception: the launch is what authorized
// them.
//
// The layers the user authors — the global config and the CLI files — always
// keep the process environment, and so do the inline specs, which are not
// expanded at all. sandboxed=false is the ordinary session contract,
// byte-for-byte Discover's; noNet is then ignored.
func DiscoverWithCLIOnlyAndEvidenceForProjectEnv(env execenv.ExecutionEnvironment, extraFiles, inlineSpecs []string, authorizedProject *LayerEvidence, sandboxed, noNet bool) ([]ServerConfig, []CLIOnlyServer, ConfigEvidence, []string, error) {
	return discoverLayers(discoverOptions{
		env:               env,
		extraFiles:        extraFiles,
		inlineSpecs:       inlineSpecs,
		trustedEnv:        processExpansionEnv(),
		authorizedProject: authorizedProject,
		sandboxed:         sandboxed,
		noNet:             noNet,
	})
}

// projectLayerEligible reports whether the project layer a workspace resolves to
// NOW is a layer the launch's startup read speaks for, so an entry whose bytes
// still match the startup record may be expanded through the process
// environment.
//
// Eligibility is about PLACE, never about the bytes at that place: the bytes are
// checked per entry, against the same single read that is then parsed. Asking
// this function whether a file "changed" would be a second, racier answer to a
// question the caller already has the bytes for.
//
// Two places are eligible:
//
//   - the recorded path itself. The launch read that exact file, whatever it
//     says now; which of its entries are authorized is the digest comparison's
//     question.
//   - the same committed file at a linked worktree's path, but only when both
//     sides are the SAME non-empty repository identity. That is a user cutting a
//     worktree delegate from a checkout whose committed .evener/mcp.json they
//     launched against: the file the child reads is the file the launch already
//     disclosed, at a second path. An empty identity on either side is not a
//     match — two checkouts discovery could not place are two unknowns, and one
//     unknown matching another would let any repository's bytes pass for the
//     launch's.
//
// Everything else — no startup record, a launch outside any repository, an
// unresolvable current path, an unrelated repository — is ineligible, and every
// entry of the layer then expands through the restricted environment.
func projectLayerEligible(authorized *LayerEvidence, env execenv.ExecutionEnvironment) bool {
	if authorized == nil || authorized.Path == "" {
		return false
	}
	currentPath := ProjectConfigPath(env)
	if currentPath == "" {
		return false
	}
	if currentPath == authorized.Path {
		return true
	}
	if authorized.RepoRoot == "" {
		return false
	}
	return projectRepoRoot(env) == authorized.RepoRoot
}

// authorizedServerDigest returns the startup digest recorded for one server
// entry, and whether a record exists for it at all.
func (e *LayerEvidence) authorizedServerDigest(name string) (string, bool) {
	if e == nil || e.ServerDigests == nil {
		return "", false
	}
	digest, ok := e.ServerDigests[name]
	return digest, ok
}

// discoverOptions is discoverLayers' input. It is a struct rather than a
// parameter list because the project layer's trust decision is not one
// environment: it is an eligibility check, a per-server digest match, and a
// network-disabled refusal, and each of those has to be visible at the call
// site that chose it.
type discoverOptions struct {
	env         execenv.ExecutionEnvironment
	extraFiles  []string
	inlineSpecs []string
	// trustedEnv answers the layers the user authors: the global config and the
	// launch/CLI files.
	trustedEnv valueexpr.Env
	// untrustedEnv answers the project layer for callers that have no per-server
	// authorization to apply — the settings probe (DiscoverWithLaunchEnv), which
	// restricts the whole layer to the launch overlay.
	untrustedEnv valueexpr.Env
	// authorizedProject is the launch's startup record of this workspace's
	// project layer. Nil authorizes nothing.
	authorizedProject *LayerEvidence
	// sandboxed selects per-server authorization for the project layer: an entry
	// is expanded through the process environment only when the layer is eligible
	// and the entry's bytes match authorizedProject.
	sandboxed bool
	// noNet refuses network-transport project servers that per-server
	// authorization did not cover, because nothing else withholds the host
	// network from them.
	noNet bool
}

// projectLayerReadOptions is the project layer's read configuration.
//
// With no per-server authorization — a root session, a settings probe — the
// whole layer is one trust class and o.untrustedEnv answers all of it. Under a
// sandboxed delegate the layer becomes one decision per entry: an eligible layer
// whose entry digest matches the launch's record expands through trustedEnv and
// is marked authorized; anything else expands through launchOverlayOnlyEnv(nil)
// and is not.
//
// The per-server digests are always recorded, whatever the caller: that map is
// what a DELEGATE this session constructs will match against, so a root session
// has to produce it even though it uses none of it itself.
func (o discoverOptions) projectLayerReadOptions(env execenv.ExecutionEnvironment) layerReadOptions {
	if !o.sandboxed {
		return layerReadOptions{
			expand: func(s string) (string, error) {
				return expandEnvVarsUntrustedInEnv(s, o.untrustedEnv)
			},
			recordServerDigests: true,
		}
	}
	eligible := projectLayerEligible(o.authorizedProject, env)
	restrictedEnv := launchOverlayOnlyEnv(nil)
	restricted := func(s string) (string, error) {
		return expandEnvVarsUntrustedInEnv(s, restrictedEnv)
	}
	return layerReadOptions{
		recordServerDigests: true,
		authorize: func(name string, raw json.RawMessage) (func(string) (string, error), bool) {
			if !eligible {
				return restricted, false
			}
			digest, ok := o.authorizedProject.authorizedServerDigest(name)
			if !ok || digest != ConfigDigest(raw) {
				return restricted, false
			}
			return func(s string) (string, error) {
				return expandEnvVarsUntrustedInEnv(s, o.trustedEnv)
			}, true
		},
	}
}

// admitProjectServers applies the network rule to a project layer's parsed
// servers and returns those this session may connect, plus a warning for each
// one it refuses.
//
// A repository's `{"type":"http","url":...}` entry is dialled by evener's own
// process, and a stdio entry is spawned through ConfineTrustedInfra, which keeps
// egress even at net=off on the grounds that MCP config the model cannot write
// is trusted infrastructure. That premise holds only for entries the launch
// authorized, so an unauthorized entry loses the exception in the way its
// transport allows:
//
//   - A stdio entry is ADMITTED, so its local tools still work, and marked
//     DenyNetworkException, so the spawn site confines it with ordinary sandbox
//     confinement and net=off severs its egress like any model-influenced spawn.
//   - With noNet, a network-transport entry is REFUSED here instead of
//     connected: the sandbox has already withheld the credentials it would
//     carry, and this is the other half — it does not get the host's network
//     either. With the network enabled there is no exception to withhold, so it
//     is admitted unchanged.
//
// Authorized servers pass through untouched, as does every server when nothing
// is sandboxed.
func (o discoverOptions) admitProjectServers(read layerRead) ([]ServerConfig, []string) {
	if !o.sandboxed {
		return read.Configs, nil
	}
	admitted := make([]ServerConfig, 0, len(read.Configs))
	var warnings []string
	for _, cfg := range read.Configs {
		switch {
		case read.Authorized[cfg.Name]:
		case !usesNetworkTransport(cfg):
			// The denial is a refusal of the carve-out, not of the server: the
			// entry still loads and still runs, under the same confinement a
			// model-authored spawn gets.
			cfg.DenyNetworkException = true
		case !o.noNet:
		default:
			warnings = append(warnings, fmt.Sprintf(
				"MCP server %q in %s was not launch-authorized, so it is not connected over the host network while this session's sandbox has network disabled",
				cfg.Name, read.Evidence.Path))
			continue
		}
		admitted = append(admitted, cfg)
	}
	return admitted, warnings
}

// usesNetworkTransport reports whether cfg reaches the network from evener's
// process or from a spawn that keeps egress: the sse and http transports, which
// the SDK dials directly, and any non-stdio entry that names a URL, so an
// unrecognized type cannot slip past the check by spelling itself differently.
func usesNetworkTransport(cfg ServerConfig) bool {
	switch cfg.Type {
	case "sse", "http":
		return true
	case "stdio", "":
		return false
	default:
		return cfg.URL != ""
	}
}

// discoverLayers is Discover's body. It reads every layer once and derives each
// layer's servers and evidence from the same bytes.
//
// The project layer is the one with more than one trust class. When o.sandboxed
// is set, each entry of it is expanded through the process environment only if
// the layer is eligible AND that entry's digest matches the startup record;
// every other entry gets launchOverlayOnlyEnv(nil), so an unanswered reference
// fails that entry alone. Otherwise the whole layer is answered by
// o.untrustedEnv, which is what the settings probe and the ordinary session
// need.
func discoverLayers(o discoverOptions) ([]ServerConfig, []CLIOnlyServer, ConfigEvidence, []string, error) {
	env := o.env
	extraFiles := o.extraFiles
	inlineSpecs := o.inlineSpecs
	trustedEnv := o.trustedEnv
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
			opts := o.projectLayerReadOptions(env)
			read, err := readLayer(projPath, opts)
			// The repository the layer belongs to is recorded on every arm,
			// absence included: it is what a sandboxed delegate's worktree
			// compares against when the same committed bytes resolve at a
			// different path (see projectLayerEligible).
			read.Evidence.RepoRoot = projectRepoRoot(env)
			evidence.Project = read.Evidence
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					warnings = append(warnings, fmt.Sprintf("MCP config %s: %v", projPath, err))
					// A layer that failed to parse is INCONCLUSIVE, not
					// established. This read dropped the whole layer, but a
					// sandboxed child parses the same file entry by entry and
					// registers every literal entry beside the broken one, so
					// the classification derived from the dropped layer (a
					// CLI-only server whose name the layer also declares) does
					// not describe what the child will find. Clearing the path
					// is what makes it say so: the evidence can no longer
					// verify, every reader treats that as unknown reachability,
					// and the CLI-only names stay ELIGIBLE for the child to try.
					//
					// Absence is the other arm and stays recorded: a layer that
					// is not there is a fact discovery established, while a
					// layer it could not read is not.
					//
					// The digest and per-server digests are kept. They were
					// recorded from the same bytes and remain true statements
					// about them; what is withdrawn is the claim that this read
					// established the layer's contents. Authorization for a
					// child keys on the path (see projectLayerEligible), so the
					// withdrawal is also the conservative direction there: a
					// layer the launch could not parse authorizes nothing.
					evidence.Project.Path = ""
				}
			} else {
				warnings = append(warnings, read.Warnings...)
				admitted, netWarnings := o.admitProjectServers(read)
				warnings = append(warnings, netWarnings...)
				layers = append(layers, admitted)
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
