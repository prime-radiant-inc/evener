# SP-A — Filesystem Plugin Discovery (Detailed Design)

Date: 2026-05-14
Status: ready for TDD implementation
Parent spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-design.md`

## 1. Goal

SP-A walks known on-disk locations at session startup and returns every plugin directory that contains a valid `.claude-plugin/plugin.json`. The result merges with the existing `--plugin-dir` flag into one ordered list of plugin paths that the loader feeds to `LoadPlugins()`. There is no registry; presence in a known directory is the install state.

SP-A replaces what the deferred SP4 install spec called "resolve enabledPlugins to cache paths." The user (or the SP-B agent skill acting on their behalf) places plugin directories where SP-A will find them; SP-A loads them.

## 2. Public API Surface

All new symbols live in package `agent`. The function follows the `DiscoverMCPConfigs` / `DiscoverSkills` naming pattern.

```go
// DiscoverPluginDirs returns absolute paths to every plugin directory
// found across the known locations, plus any CLI-supplied directories.
// The returned slice is ordered: lowest-precedence first.
//
// Discovery sources (lowest to highest precedence):
//   1. ~/.config/serf/plugins/<name>/   (per-user)
//   2. <git-root>/.serf/plugins/<name>/ (per-project; uses gitRootOrEmpty)
//   3. extraDirs                          (e.g., --plugin-dir paths, in order)
//
// A plugin directory qualifies if it contains .claude-plugin/plugin.json.
// Directories without a manifest are silently skipped. Manifest parse
// errors are reported via the returned []error (one per failure) but do
// not abort discovery.
//
// Symlinks are followed via filepath.EvalSymlinks. Cycles error per dir
// and are reported in the returned []error.
//
// On plugin-name collision across sources, the higher-precedence source
// wins; the loser is reported via the returned []DiscoveryShadowedEntry.
func DiscoverPluginDirs(env ExecutionEnvironment, extraDirs []string) ([]string, []DiscoveryShadowedEntry, []error)

// DiscoveryShadowedEntry records a plugin-name collision that SP-A
// resolved by precedence. SP8's startup uses this to emit one
// "shadowed plugin" warning per collision.
type DiscoveryShadowedEntry struct {
    Name       string
    KeptDir    string
    SkippedDir string
}
```

`DiscoverPluginDirs` is consumed by `SessionConfig` bootstrap (see SP8). Its output replaces the current bare-`--plugin-dir` slice with a unioned slice.

## 3. Discovery Algorithm

```
inputs:  env (for cwd + git root), extraDirs []string
outputs: dirs []string (absolute, in precedence order), shadowed [], errs []

1. roots = []                                         # absolute parent directories
2. globalPath = userConfigDir() + "/serf/plugins"     # ~/.config/serf/plugins
   if globalPath exists:
       roots.append(globalPath)
3. projectRoot = gitRootOrEmpty(env, env.WorkingDirectory())
   if projectRoot != "":
       projectPath = projectRoot + "/.serf/plugins"
       if projectPath exists:
           roots.append(projectPath)
4. raw = []                                           # (name, absDir) tuples
   for each root in roots (order = precedence ascending):
       for each entry in ReadDir(root):
           if entry is not a directory and not a symlink to one: skip
           manifestPath = root + "/" + entry.Name + "/.claude-plugin/plugin.json"
           if !Exists(manifestPath): skip
           absDir, err = filepath.EvalSymlinks(root + "/" + entry.Name)
           if err: errs.append(err); continue
           raw.append((entry.Name, absDir))
5. for each path in extraDirs (CLI --plugin-dir, in flag order):
       abs, err = absoluteAndEvalSymlinks(path)
       if err: errs.append(err); continue
       manifest = abs + "/.claude-plugin/plugin.json"
       if !Exists(manifest):
           errs.append(fmt.Errorf("--plugin-dir %q: no manifest", path))
           continue
       name, err = readManifestName(manifest)
       if err: errs.append(err); continue
       raw.append((name, abs))
6. # Resolve collisions: walk raw in order, later wins.
   seen = map[name]int          # name -> index in dirs
   dirs = []
   for (name, dir) in raw:
       if i, ok := seen[name]:
           shadowed.append({Name: name, KeptDir: dir, SkippedDir: dirs[i]})
           dirs[i] = dir
       else:
           seen[name] = len(dirs); dirs.append(dir)
7. return dirs, shadowed, errs
```

The precedence-ascending walk in step 4 plus the later-wins rule in step 6 gives: user-plugin < project-plugin < `--plugin-dir`. Matches the parent design's stated ordering.

## 4. File-Format Details

- A plugin directory is identified by `<dir>/.claude-plugin/plugin.json`. No fallback file (e.g. `plugin.json` at root) is recognized — match Claude Code's documented contract.
- Symlinks at the plugin-directory level resolve via `filepath.EvalSymlinks` so users can develop a plugin elsewhere and `ln -s` it into `~/.config/serf/plugins/foo`.
- A plugin directory whose manifest is malformed JSON or fails `ParsePluginManifest` produces one entry in `errs` and does not enter `dirs`.
- A plugin directory whose name (per the manifest) differs from its directory basename is allowed; collisions are resolved by manifest `name`, not directory basename.

## 5. Validation

Beyond what `ParsePluginManifest` already validates:

- Each `extraDir` (CLI) must point to a directory that contains the manifest; if not, `errs` records `"--plugin-dir <path>: no manifest"`.
- Symlink cycles record an error per offending directory.
- Permission errors on a parent root (e.g., `~/.config/serf/plugins` is unreadable) record one error and skip the root; do not abort.

## 6. Error Contracts

- Returns `(dirs, shadowed, errs)`. `dirs` is always usable even when `errs` is non-empty. SP8 prints each error as one line to stderr at startup, then continues.
- Returns `(nil, nil, nil)` when no roots exist and no `extraDirs` were passed. Not an error.

## 7. Package and File Layout

New files in `agent/`:

- `plugin_discovery.go` — `DiscoverPluginDirs`, `DiscoveryShadowedEntry`, internal helpers
- `plugin_discovery_test.go` — table-driven tests, real filesystem via `t.TempDir()`

Existing files:

- `agent/skills.go` — re-use `gitRootOrEmpty` and `dirsFromRootToCwd` style helpers; refactor only if duplication becomes unwieldy

## 8. Testing Strategy

Tests are the contract. List first, implementation second.

| # | Name | Scenario | Expected |
|---|---|---|---|
| 1 | empty everywhere | No roots, no extraDirs | (nil, nil, nil) |
| 2 | global only | One plugin in ~/.config/serf/plugins/foo/ | dirs=[foo], no shadowed, no errs |
| 3 | project only | One plugin in <root>/.serf/plugins/bar/ | dirs=[bar] |
| 4 | global+project distinct | foo global, bar project | dirs=[foo, bar] (global first) |
| 5 | project shadows global | foo in both global and project | dirs=[project-foo], shadowed=[{foo, project, global}] |
| 6 | plugin-dir shadows project | --plugin-dir foo + project foo | dirs=[--plugin-dir foo], shadowed=[{foo, --plugin-dir, project}] |
| 7 | plugin-dir order | Two --plugin-dir flags with same plugin name | last flag wins, shadowed records prior |
| 8 | no manifest | global subdir with no `.claude-plugin/plugin.json` | silently skipped, no errs |
| 9 | malformed manifest | global subdir with invalid JSON | errs=[parse error], dirs excludes it |
| 10 | symlink to elsewhere | Plugin via symlink in global | dirs=[resolved abs path] |
| 11 | symlink cycle | Symlink loop | errs=[cycle error], skipped |
| 12 | unreadable root | `chmod 0` on global root | errs=[permission], project still loads |
| 13 | --plugin-dir without manifest | --plugin-dir /tmp/foo where /tmp/foo lacks manifest | errs=[--plugin-dir: no manifest] |
| 14 | --plugin-dir with non-existent path | --plugin-dir /nonexistent | errs=[stat error] |
| 15 | no git root | env.WorkingDirectory() not in a git repo | project source skipped silently |
| 16 | XDG_CONFIG_HOME override | Set XDG_CONFIG_HOME to t.TempDir() | global source resolves under temp |
| 17 | manifest name differs from dirname | dir is `xyz`, manifest name is `foo` | dirs=[xyz]; collisions resolve by manifest name |
| 18 | empty plugin-dir slice | extraDirs=[] | only roots considered |

Fixtures under `agent/testdata/plugin-discovery/` provide minimal valid and malformed plugin trees.

Conventions:
- Use `t.TempDir()` for every test; never share state across tests.
- Use `t.Setenv("HOME", ...)` and `t.Setenv("XDG_CONFIG_HOME", ...)` to redirect the global root.
- For project source, create a `.git/` directory inside `t.TempDir()` and `cd` to it (or pass an `env` whose `WorkingDirectory()` reports the temp path).
- Construct the `env` argument via the same `fakeEnv` helper used by existing skill discovery tests.

## 9. Open Questions

1. **Should symlink resolution happen on the parent root, or only on each plugin entry?**
   Recommendation: only on each plugin entry. Resolving the parent root means `~/.config/serf/plugins` could itself be a symlink to a shared team-managed directory — useful — but rarely needed. Per-entry resolution is enough for the common case (symlinking individual plugins for dev). If asked, expand later.

2. **What happens when both `~/.config/serf/plugins/foo/` exists AND `--plugin-dir` points to a path that resolves to the same absolute directory (same plugin via two routes)?**
   Recommendation: detect duplicate abs paths after EvalSymlinks; emit a shadowed entry (not an error) and keep the higher-precedence entry once. Document this so users who symlink rather than rm see consistent behavior.

3. **Does discovery rescan mid-session?**
   No. Discovery runs once at session startup. To pick up newly staged plugins, the user restarts the session (or, after SP-B lands, the skill tells them to). Live reload is out of scope.
