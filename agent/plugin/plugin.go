// Package plugin loads Claude Code-style plugins from disk — their manifest,
// skills, subagents, hooks, and MCP server configs — into typed values the
// agent engine consumes. It also parses per-project plugin settings.
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/skill"
)

var (
	pluginAbs = filepath.Abs
	// pluginReadFile is the single whole-file read the loaders in this package
	// go through (manifests, hooks files, .mcp.json, plugin and evener-wide
	// component files). It must refuse a FIFO or device instead of parking the
	// load on it.
	pluginReadFile         = execenv.ReadFileCapped
	pluginReadDir          = os.ReadDir
	pluginStat             = os.Stat
	pluginDiscoverAgents   = discoverPluginAgents
	pluginDiscoverCommands = discoverPluginCommands
	pluginDiscoverHooks    = discoverPluginHooksDiag
	pluginDiscoverMCP      = discoverPluginMCPConfigs
)

// SkippedComponent names a command or agent file a plugin load skipped and why.
type SkippedComponent struct {
	Kind   string // "command" or "agent"
	File   string // absolute path of the skipped file
	Reason string
}

// Manifest represents a parsed plugin.json file.
// Fields like Author, Commands, Agents, Hooks, and MCPServers use
// json.RawMessage because their shapes vary (string, array, or object).
type Manifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version,omitempty"`
	Description string          `json:"description,omitempty"`
	Author      json.RawMessage `json:"author,omitempty"`
	Homepage    string          `json:"homepage,omitempty"`
	Repository  string          `json:"repository,omitempty"`
	License     string          `json:"license,omitempty"`
	Keywords    []string        `json:"keywords,omitempty"`
	Commands    json.RawMessage `json:"commands,omitempty"`
	Agents      json.RawMessage `json:"agents,omitempty"`
	Hooks       json.RawMessage `json:"hooks,omitempty"`
	MCPServers  json.RawMessage `json:"mcpServers,omitempty"` //nolint:tagliatelle // upstream .mcp.json key spelling
}

// kebabCaseRe matches valid kebab-case names: lowercase alphanumeric,
// optionally separated by single hyphens, no leading/trailing hyphens.
var kebabCaseRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validatePluginName checks that name is non-empty kebab-case
// (lowercase alphanumeric with hyphens, no leading/trailing hyphens).
func validatePluginName(name string) error {
	if name == "" {
		return errors.New("plugin name must not be empty")
	}
	if !kebabCaseRe.MatchString(name) {
		return fmt.Errorf("plugin name %q must be kebab-case (lowercase alphanumeric with hyphens, no leading/trailing hyphens)", name)
	}
	return nil
}

// ParseManifest unmarshals JSON plugin manifest data and validates
// the required name field.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parsing plugin manifest: %w", err)
	}
	if err := validatePluginName(m.Name); err != nil {
		return Manifest{}, fmt.Errorf("invalid plugin manifest: %w", err)
	}
	return m, nil
}

// expandPluginRoot replaces plugin-root placeholders with pluginDir in s.
func expandPluginRoot(s string, pluginDir string) string {
	s = strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", pluginDir)
	return strings.ReplaceAll(s, "${PLUGIN_ROOT}", pluginDir)
}

// expandPluginRootExpr is expandPluginRoot for text the shared
// $-expression parser reads afterwards (the MCP config surfaces): a plugin
// directory whose own path carries a $ would otherwise become expression
// material, so the substituted path's dollars are escaped and the parser
// emits them as literal text. The hook surfaces run the substituted text
// through a shell, not the parser, and keep expandPluginRoot.
func expandPluginRootExpr(s string, pluginDir string) string {
	return expandPluginRoot(s, strings.ReplaceAll(pluginDir, "$", "$$"))
}

// Instance represents a plugin that has been loaded from disk.
type Instance struct {
	Manifest Manifest // parsed plugin.json
	Dir      string   // absolute path = CLAUDE_PLUGIN_ROOT
	// ManifestFlavor is which manifest directory the plugin was loaded from:
	// "claude" for .claude-plugin, "codex" for .codex-plugin. A plugin that
	// ships both flavors loads the Claude one (see Load); the flavor is surfaced
	// at load time so a wrong-flavor load (e.g. codex hooks that re-inject on
	// resume) is diagnosable.
	ManifestFlavor string
	// ManifestPath is the absolute path of the plugin.json that was loaded.
	ManifestPath string
	Skills       map[string]skill.SkillMeta     // namespaced as "plugin-name:skill-name"
	Agents       map[string]Agent               // namespaced as "plugin-name:agent-name"
	Commands     map[string]Command             // namespaced as "plugin-name:command-name"
	Hooks        map[HookEvent][]RegisteredHook // keyed by event type
	MCPConfigs   []mcpconfig.ServerConfig       // namespaced as "plugin_<name>_<server>"
	// MCPEvidence is the configuration evidence for the MCP layers this load
	// actually parsed, captured from the SAME bytes it parsed rather than by
	// reading the files again: a second read could pair these configs with a
	// digest from a different revision of the file.
	//
	// It always carries the selected manifest, INCLUDING when the manifest
	// declares no inline mcpServers — "no inline layer" is a fact that an
	// operator adding one later changes, and a child re-parses the manifest at
	// spawn. It carries the .mcp.json layer's read, absence, or unreadability on
	// the terms mcpconfig.ConfigEvidence documents.
	MCPEvidence []mcpconfig.LayerEvidence
	// MCPConfigWarnings names non-fatal MCP config problems discovered while
	// loading this plugin (an unreadable/malformed .mcp.json file, malformed
	// inline mcpServers JSON, or an inline entry that fails validation): the
	// affected layer is skipped, but the plugin's skills/agents/hooks still load.
	MCPConfigWarnings []string
	// SkippedComponents lists the command and agent files that could not be
	// read or parsed. Each costs only itself: the plugin's other commands,
	// agents, skills, hooks and MCP servers still load.
	SkippedComponents []SkippedComponent

	// UnsupportedHooks is the set of Claude-recognized events declared by this
	// plugin that evener does not currently fire (tier: reserved-placeholder).
	// Populated by Load; empty when no such events are declared.
	UnsupportedHooks map[HookEvent]bool
	// UnknownHooks is the set of event names declared by this plugin that evener
	// does not recognize as Claude events at all.
	UnknownHooks map[string]bool
}

// discoverPluginSkills scans a plugin's skills directories and returns
// skills namespaced as "pluginName:skillName".
func discoverPluginSkills(pluginDir, pluginName string) map[string]skill.SkillMeta {
	dirs := resolveComponentDirs(pluginDir, "skills", nil)
	raw := map[string]skill.SkillMeta{}
	for _, dir := range dirs {
		skill.ScanSkillsDir(dir, raw)
	}
	namespaced := make(map[string]skill.SkillMeta, len(raw))
	for name, meta := range raw {
		namespaced[pluginName+":"+name] = meta
	}
	return namespaced
}

// discoverPluginMCPConfigs reads MCP server configs from a plugin's .mcp.json
// file and/or inline manifest mcpServers field. Server names are prefixed with
// "plugin_<pluginName>_" and ${CLAUDE_PLUGIN_ROOT} is expanded to pluginDir.
// Both layers parse untrusted — plugin content is third-party, and expansion
// runs commands on the host — so a $(command) expression is refused at load
// like any other config-parse failure.
//
// Config-layer problems — an unreadable/malformed .mcp.json file, malformed
// inline mcpServers JSON, or an inline entry that fails ParseServerMap
// validation (empty name, unset ${VAR}) — degrade to an entry in the returned
// warnings slice instead of aborting the plugin load; the affected layer is
// simply skipped. The error return is reserved for failures none of these
// config-parse paths produce.
// It also returns the evidence for the .mcp.json layer it parsed, taken from
// the SAME read that supplied the configs.
//
// The two must come from one read. Opening the file again to fingerprint it
// lets a plugin update land between the reads: the parent then records the old
// server list beside the new digest, every later check accepts that digest, and
// the ceiling keeps excluding a fallback tool a fresh child registers.
//
// The evidence keeps absent and unverifiable apart exactly as
// mcpconfig.ConfigEvidence documents: an absent file records its path with an
// empty digest (its later appearance is what invalidates the evidence), and a
// read or parse that failed for any other reason records nothing, which every
// caller reads as unknown reachability.
func discoverPluginMCPConfigs(pluginDir string, manifestMCPServers json.RawMessage, pluginName string) ([]mcpconfig.ServerConfig, []string, mcpconfig.LayerEvidence, error) {
	var layers [][]mcpconfig.ServerConfig
	var warnings []string

	// Layer 1: .mcp.json file in the plugin directory.
	mcpPath := filepath.Join(pluginDir, ".mcp.json")
	var fileEvidence mcpconfig.LayerEvidence
	fileConfigs, fileBytes, err := loadPluginMCPFile(mcpPath, pluginDir)
	switch {
	case err == nil:
		layers = append(layers, fileConfigs)
		fileEvidence = mcpconfig.LayerEvidence{Path: mcpPath, Digest: mcpconfig.ConfigDigest(fileBytes)}
	case os.IsNotExist(err):
		// Missing file is not an error.
		fileEvidence = mcpconfig.LayerEvidence{Path: mcpPath}
	default:
		warnings = append(warnings, fmt.Sprintf("plugin %q: MCP config failed: %v", pluginName, err))
	}

	// Layer 2: Inline mcpServers from the manifest.
	if len(manifestMCPServers) > 0 {
		expanded := expandPluginRootExpr(string(manifestMCPServers), pluginDir)
		var servers map[string]json.RawMessage
		if err := json.Unmarshal([]byte(expanded), &servers); err != nil {
			warnings = append(warnings, fmt.Sprintf("plugin %q: MCP config failed: %v", pluginName, err))
		} else if len(servers) > 0 {
			inlineConfigs, err := mcpconfig.ParseServerMapUntrusted(servers, "inline")
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("plugin %q: MCP config failed: %v", pluginName, err))
			} else {
				layers = append(layers, inlineConfigs)
			}
		}
	}

	merged := mcpconfig.Merge(layers...)
	if len(merged) == 0 {
		return nil, warnings, fileEvidence, nil
	}

	// Namespace server names.
	prefix := "plugin_" + pluginName + "_"
	for i := range merged {
		merged[i].Name = prefix + merged[i].Name
	}
	return merged, warnings, fileEvidence, nil
}

// loadPluginMCPFile reads a plugin's .mcp.json file, expands
// ${CLAUDE_PLUGIN_ROOT} in the raw JSON, then parses server configs.
//
// It returns the bytes it read so the caller can fingerprint exactly the
// revision it parsed. A caller that reads the file again for a digest is
// fingerprinting a file it did not parse: a plugin update landing between the
// two reads pairs the old server list with the new digest, every later
// freshness check accepts that pair, and the ceiling goes on excluding a
// fallback tool the child does register.
func loadPluginMCPFile(path, pluginDir string) ([]mcpconfig.ServerConfig, []byte, error) {
	data, err := pluginReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	// Expand ${CLAUDE_PLUGIN_ROOT} before env-var expansion so
	// expandEnvVars (called by serverJSONToConfig) doesn't fail on it,
	// with the substituted path's dollars escaped so a plugin directory
	// containing a $ reads as literal text, not expression material.
	expanded := expandPluginRootExpr(string(data), pluginDir)

	var cf struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"` //nolint:tagliatelle // upstream .mcp.json key spelling
	}
	if err := json.Unmarshal([]byte(expanded), &cf); err != nil {
		return nil, nil, fmt.Errorf("parsing MCP config %s: %w", path, err)
	}

	configs, err := mcpconfig.ParseServerMapUntrusted(cf.MCPServers, path)
	if err != nil {
		return nil, nil, err
	}
	return configs, data, nil
}

// Load reads a plugin manifest from <dir>/.claude-plugin/plugin.json, falling
// back to <dir>/.codex-plugin/plugin.json only when no Claude manifest exists,
// parses it, and returns an Instance with Dir set to the resolved absolute path.
// Claude is preferred because evener preserves conversation context across resume
// (it replays the transcript), matching Claude Code's resume semantics; the
// codex flavor's SessionStart hooks re-inject on resume, which double-injects
// under evener. The chosen flavor is recorded in Instance.ManifestFlavor.
func Load(dir string) (Instance, error) {
	resolution, err := resolveManifest(dir)
	if err != nil {
		return Instance{}, err
	}
	return loadResolution(resolution)
}

// loadResolution builds a plugin from a manifest resolution that has ALREADY
// been read, so a batch loader's duplicate selection and the plugin it admits
// are the same read.
//
// Resolving twice is not equivalent to resolving once. A directory is a path
// that can be retargeted by an upgrade: if the symlink moves between the read
// that reserved a manifest name and the read that produced the instance, the
// batch has reserved the OLD name while the loaded plugin supplies the NEW
// plugin's servers. A later directory holding the old name is then skipped as a
// duplicate of a name nothing supplies, and the evidence recorded for both
// directories can keep matching, so nothing later notices. One resolution
// cannot disagree with itself.
func loadResolution(resolution manifestResolution) (Instance, error) {
	resolved, manifestPath, flavor := resolution.ResolvedDir, resolution.ManifestPath, resolution.Flavor
	absentPreferredManifest := resolution.AbsentPreferredManifest
	data := resolution.ManifestBytes
	manifest, err := ParseManifest(data)
	if err != nil {
		return Instance{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}

	lp := Instance{Manifest: manifest, Dir: resolved, ManifestFlavor: flavor, ManifestPath: manifestPath}
	// The manifest is always a layer: a child re-parses it at spawn, and a
	// manifest that declares no inline mcpServers today can declare one later.
	// The digest is of the bytes parsed just above, so an edit during load
	// cannot pair this config with a digest from another revision.
	lp.MCPEvidence = []mcpconfig.LayerEvidence{{
		Path:   manifestPath,
		Digest: mcpconfig.ConfigDigest(data),
	}}
	lp.Skills = discoverPluginSkills(resolved, manifest.Name)

	agents, skippedAgents, err := pluginDiscoverAgents(resolved, manifest.Agents, manifest.Name)
	if err != nil {
		return Instance{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}
	lp.Agents = agents

	commands, skippedCommands, err := pluginDiscoverCommands(resolved, manifest.Commands, manifest.Name)
	if err != nil {
		return Instance{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}
	lp.Commands = commands
	lp.SkippedComponents = slices.Concat(skippedAgents, skippedCommands)

	hooks, unsupportedHooks, unknownHooks, err := pluginDiscoverHooks(resolved, manifest.Hooks, manifest.Name)
	if err != nil {
		return Instance{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}
	lp.Hooks = hooks
	lp.UnsupportedHooks = unsupportedHooks
	lp.UnknownHooks = unknownHooks

	mcpConfigs, mcpWarnings, mcpFileEvidence, err := pluginDiscoverMCP(resolved, manifest.MCPServers, manifest.Name)
	if err != nil {
		return Instance{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}
	lp.MCPConfigs = mcpConfigs
	lp.MCPConfigWarnings = mcpWarnings
	lp.MCPEvidence = append(lp.MCPEvidence, mcpFileEvidence)
	// A Codex-flavored load recorded the absence of the Claude manifest above,
	// and that absence is part of the evidence rather than a non-event: the two
	// manifests can declare different inline mcpServers, so a later add of the
	// preferred Claude manifest changes which servers a child parses at spawn.
	// Recording the resolved path with an empty digest marks it as CONFIRMED
	// ABSENT, so its appearance invalidates the evidence and leaves the affected
	// names eligible. This is the same rule as the absent .mcp.json layer: absent
	// is a path with no digest, and unreadable is no path at all.
	if absentPreferredManifest != "" {
		lp.MCPEvidence = append(lp.MCPEvidence, mcpconfig.LayerEvidence{Path: absentPreferredManifest})
	}
	// Every layer above is a file found by RESOLVING the configured plugin
	// directory, so each carries that configured directory beside its resolved
	// path. A child resolves it again at spawn: if an upgrade retargets a
	// symlinked plugin directory and leaves the old version on disk, the recorded
	// digests still match under the old target while the child reads the new one,
	// and the ceiling would go on excluding a tool the upgrade made reachable.
	for i := range lp.MCPEvidence {
		lp.MCPEvidence[i].ConfiguredPath = resolution.ConfiguredDir
		lp.MCPEvidence[i].ResolvedDir = resolved
	}

	return lp, nil
}

// ManifestName resolves and validates only a plugin manifest. Batch callers
// use it to apply the same first-manifest-wins duplicate policy before any
// component loader runs, including callers that intentionally load skills
// without agents, commands, hooks, or MCP configuration.
func ManifestName(dir string) (string, error) {
	resolved, err := resolveManifest(dir)
	return resolved.Name, err
}

// manifestResolution is what identifying a plugin directory read: its manifest
// name, the directory the configured path resolved to, and the exact manifest
// bytes that name came from.
//
// The name alone is not enough to remember. A directory skipped as a duplicate
// can be RETARGETED by an upgrade to a different plugin, and only the
// resolution and the bytes as they were then can tell that the directory now
// answers differently.
type manifestResolution struct {
	Name           string
	ResolvedDir    string
	ManifestPath   string
	ManifestDigest string
	// AbsentPreferredManifest is the higher-priority manifest this resolution
	// looked for and CONFIRMED missing, when selection fell back to the other
	// flavor. A child re-runs this selection, so a preferred manifest appearing
	// later changes which plugin the directory supplies — including a duplicate
	// skipped on an earlier read. Recording the confirmed absence is what makes
	// its appearance a change instead of an unchanged digest.
	AbsentPreferredManifest string
	// Flavor is which manifest directory supplied the manifest above, and
	// ManifestBytes are the exact bytes that were parsed. Both are carried so a
	// loader can build the instance from THIS read rather than resolving again:
	// a second resolution can pick a different flavor, or different bytes, if
	// the directory is retargeted in between.
	Flavor        string
	ManifestBytes []byte
	// ConfiguredDir is the path the caller named, before any symlink was
	// followed. Evidence records it beside the resolved directory because a
	// child resolves the configured path again at spawn: a retargeted symlink
	// keeps every digest matching while pointing somewhere else.
	ConfiguredDir string
}

func resolveManifest(dir string) (manifestResolution, error) {
	var out manifestResolution
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return out, fmt.Errorf("resolving plugin dir %q: %w", dir, err)
	}
	resolved, err = pluginAbs(resolved)
	if err != nil {
		return out, fmt.Errorf("resolving plugin dir %q: %w", dir, err)
	}

	manifestPath := filepath.Join(resolved, ".claude-plugin", "plugin.json")
	var absentPreferred string
	if _, err := pluginStat(manifestPath); err != nil {
		if !os.IsNotExist(err) {
			return out, fmt.Errorf("reading plugin manifest %q: %w", manifestPath, err)
		}
		absentPreferred = manifestPath
		manifestPath = filepath.Join(resolved, ".codex-plugin", "plugin.json")
	}
	data, err := pluginReadFile(manifestPath)
	if err != nil {
		return out, fmt.Errorf("reading plugin manifest %q: %w", manifestPath, err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return out, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}
	flavor := "claude"
	if absentPreferred != "" {
		flavor = "codex"
	}
	return manifestResolution{
		Name:                    manifest.Name,
		ResolvedDir:             resolved,
		ManifestPath:            manifestPath,
		ManifestDigest:          mcpconfig.ConfigDigest(data),
		AbsentPreferredManifest: absentPreferred,
		Flavor:                  flavor,
		ManifestBytes:           data,
		ConfiguredDir:           dir,
	}, nil
}

// SkippedPlugin describes a plugin directory LoadAllFailSoft skipped instead
// of failing the whole batch, and why.
type SkippedPlugin struct {
	Dir    string // the directory that was skipped
	Name   string // the manifest name, set only when the skip was a duplicate (empty when Load itself failed)
	Reason string // human-readable reason, suitable for a user-facing warning
	// Evidence is what identified a DUPLICATE's directory: the manifest it
	// resolved to and the bytes the name came from. A caller recording
	// freshness evidence for the plugins it loaded must record this too, or a
	// retargeted skip is invisible — the supplying directory is unchanged, so
	// its evidence still matches, while the skipped directory now names a
	// different plugin with a matching server that the next child loads.
	//
	// Zero for a BROKEN skip: ManifestName failed, so there is no resolution to
	// remember, and the caller records unverifiable evidence instead.
	Evidence mcpconfig.LayerEvidence
	// AbsentPreferredManifest is the higher-priority manifest this duplicate's
	// resolution confirmed missing. Recorded because a child re-runs manifest
	// selection: a preferred manifest appearing later makes the directory supply
	// a different plugin, and only a recorded absence turns that into a change.
	AbsentPreferredManifest mcpconfig.LayerEvidence
}

// LoadAllFailSoft loads each directory in dirs independently and never aborts
// the batch: a directory that fails to load or duplicates an already-selected plugin manifest name is recorded in the
// returned skipped slice and passed over, instead of failing every other
// directory too. Duplicate selection is first-manifest-wins, even when that
// first plugin later fails an unrelated component load; this gives all batch
// callers a deterministic selection policy that skill-only readers can share.
// Use this in batch contexts — session init, the hub's command catalog —
// where one broken or duplicate directory must not brick everything else.
func LoadAllFailSoft(dirs []string) ([]Instance, []SkippedPlugin) {
	plugins := make([]Instance, 0, len(dirs))
	seen := make(map[string]string) // plugin name -> dir already loaded
	var skipped []SkippedPlugin

	for _, dir := range dirs {
		resolution, err := resolveManifest(dir)
		manifestName := resolution.Name
		if err != nil {
			skipped = append(skipped, SkippedPlugin{
				Dir:    dir,
				Reason: fmt.Sprintf("skipping broken plugin at %q: %v", dir, err),
			})
			continue
		}
		if prevDir, ok := seen[manifestName]; ok {
			duplicate := SkippedPlugin{
				Dir:    dir,
				Name:   manifestName,
				Reason: fmt.Sprintf("skipping duplicate plugin name %q at %q (already selected from %q)", manifestName, dir, prevDir),
				Evidence: mcpconfig.LayerEvidence{
					Path:           resolution.ManifestPath,
					Digest:         resolution.ManifestDigest,
					ConfiguredPath: dir,
					ResolvedDir:    resolution.ResolvedDir,
				},
			}
			// The preferred manifest this resolution CONFIRMED absent is a fact a
			// child can change: it re-runs the selection and would then read that
			// manifest instead, supplying a plugin this skip never described.
			if resolution.AbsentPreferredManifest != "" {
				duplicate.AbsentPreferredManifest = mcpconfig.LayerEvidence{Path: resolution.AbsentPreferredManifest}
			}
			skipped = append(skipped, duplicate)
			continue
		}
		seen[manifestName] = dir
		lp, err := loadResolution(resolution)
		if err != nil {
			skipped = append(skipped, SkippedPlugin{
				Dir:    dir,
				Reason: fmt.Sprintf("skipping broken plugin at %q: %v", dir, err),
			})
			continue
		}
		plugins = append(plugins, lp)
	}
	return plugins, skipped
}

// MergeCommands flattens plugin instances' commands (namespaced keys) and
// overlays evener-wide commands (bare keys), returning the unified command
// map a session or the hub catalog resolves against. Bare keys can never
// collide with "plugin:name" keys (evener-wide discovery rejects colons), so
// the overlay cannot shadow a plugin's qualified key; precedence between a
// bare evener-wide command and a plugin's bare-name fallback is decided by
// ResolveCommand's exact-match-first rule.
func MergeCommands(instances []Instance, evenerwide map[string]Command) map[string]Command {
	out := make(map[string]Command, len(evenerwide))
	for _, inst := range instances {
		maps.Copy(out, inst.Commands)
	}
	maps.Copy(out, evenerwide)
	return out
}

// resolveComponentDirs returns a list of absolute directory paths to scan for
// a component type. It always includes <pluginDir>/<defaultName>/ if that
// directory exists on disk. If override is a string, it is resolved relative to
// pluginDir and added. If override is a []any of strings, each is resolved and
// added. Custom paths supplement defaults, they don't replace them.
func resolveComponentDirs(pluginDir string, defaultName string, override any) []string {
	var dirs []string

	// Include the default dir if it exists on disk.
	defaultDir := filepath.Join(pluginDir, defaultName)
	if info, err := pluginStat(defaultDir); err == nil && info.IsDir() {
		dirs = append(dirs, defaultDir)
	}

	// Resolve custom override paths relative to pluginDir.
	switch v := override.(type) {
	case string:
		dirs = append(dirs, filepath.Join(pluginDir, v))
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				dirs = append(dirs, filepath.Join(pluginDir, s))
			}
		}
	}

	return dirs
}

// componentMarkdownFiles expands resolved component paths into .md file paths.
// A path may be a directory (scanned non-recursively for .md files) or an
// individual .md file — Claude Code manifests allow both forms (e.g.
// "agents": ["./agents/browser-user.md"]). Missing paths are skipped.
func componentMarkdownFiles(paths []string) ([]string, error) {
	var files []string
	for _, p := range paths {
		absolute, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("resolving component path %q: %w", p, err)
		}
		p = absolute
		info, err := pluginStat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading component path %q: %w", p, err)
		}
		if !info.IsDir() {
			if strings.HasSuffix(p, ".md") {
				files = append(files, p)
			}
			continue
		}
		entries, err := pluginReadDir(p)
		if err != nil {
			return nil, fmt.Errorf("reading component dir %q: %w", p, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			files = append(files, filepath.Join(p, entry.Name()))
		}
	}
	return files, nil
}
