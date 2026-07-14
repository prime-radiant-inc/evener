package launchconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"primeradiant.com/serf/identifier"
)

// Resolve loads and merges every layer for the given cwd, applying the
// per-launch override on top. stateRoot is typically ~/.serf. The repo
// layer is honored only when its trust state is "trusted".
func Resolve(stateRoot, cwd string, overrides Layer) (Resolved, error) {
	return resolveFS(afero.NewOsFs(), stateRoot, cwd, overrides)
}

func resolveFS(fs afero.Fs, stateRoot, cwd string, overrides Layer) (Resolved, error) {
	paths, err := PathsFor(stateRoot, cwd)
	if err != nil {
		return Resolved{}, fmt.Errorf("resolve project: %w", err)
	}
	layers := map[LayerName]Layer{}
	var pathDiags []Diagnostic

	g, err := loadLayerFS(fs, paths.Global)
	if err != nil {
		return Resolved{}, fmt.Errorf("global: %w", err)
	}
	g = validateAbsolutePaths(LayerGlobal, g, &pathDiags)
	layers[LayerGlobal] = g

	// In-repo: load + hash + trust check.
	repoStatus, repoLayer, repoDiags := loadRepoLayerFS(fs, cwd, stateRoot, paths.Project)
	if repoStatus != nil && repoStatus.Trust == TrustTrusted {
		layers[LayerRepo] = repoLayer
	}

	p, projectDiags, err := loadProjectLayerFS(fs, paths)
	if err != nil {
		return Resolved{}, fmt.Errorf("project: %w", err)
	}
	p = validateAbsolutePaths(LayerProject, p, &pathDiags)
	layers[LayerProject] = p

	layers[LayerLaunch] = overrides

	resolved, _ := mergeLayers(layers)
	resolved.Repo = repoStatus
	resolved.Diagnostics = append(resolved.Diagnostics, pathDiags...)
	resolved.Diagnostics = append(resolved.Diagnostics, repoDiags...)
	resolved.Diagnostics = append(resolved.Diagnostics, projectDiags...)
	return resolved, nil
}

// LoadProjectLayer reads the local project layer. The canonical path is
// <cwd>/.serf/launch.local.toml; the old hub-state path remains a read-only
// fallback so existing project defaults continue to apply until the layer is
// saved again.
func LoadProjectLayer(paths Paths) (Layer, []Diagnostic, error) {
	return loadProjectLayerFS(afero.NewOsFs(), paths)
}

func loadProjectLayerFS(fs afero.Fs, paths Paths) (Layer, []Diagnostic, error) {
	if _, err := fs.Stat(paths.ProjectFile); err == nil {
		layer, err := loadLayerFS(fs, paths.ProjectFile)
		return layer, nil, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return Layer{}, nil, err
	}

	if _, err := fs.Stat(paths.LegacyProject); err == nil {
		layer, err := loadLayerFS(fs, paths.LegacyProject)
		if err != nil {
			return Layer{}, nil, err
		}
		return layer, []Diagnostic{{
			Layer:   LayerProject,
			Field:   "launch.local.toml",
			Message: fmt.Sprintf("using legacy project launch config at %s; save the project layer to migrate to %s", paths.LegacyProject, paths.ProjectFile),
		}}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Layer{}, nil, err
	}

	return Layer{}, nil, nil
}

func loadRepoLayer(cwd, stateRoot string) (*RepoStatus, Layer, []Diagnostic) {
	project, err := identifier.ResolveProject(cwd)
	if err != nil {
		return &RepoStatus{Path: filepath.Join(cwd, ".serf", "launch.toml"), Trust: TrustUntrusted}, Layer{}, []Diagnostic{{Layer: LayerRepo, Field: ".serf/launch.toml", Message: err.Error()}}
	}
	return loadRepoLayerFS(afero.NewOsFs(), cwd, stateRoot, project)
}

func loadRepoLayerFS(fs afero.Fs, cwd, stateRoot string, project identifier.Project) (*RepoStatus, Layer, []Diagnostic) {
	repoPath := filepath.Join(cwd, ".serf", "launch.toml")
	data, err := afero.ReadFile(fs, repoPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &RepoStatus{Path: repoPath, Trust: TrustAbsent}, Layer{}, nil
		}
		return &RepoStatus{Path: repoPath, Trust: TrustAbsent}, Layer{}, []Diagnostic{{
			Layer: LayerRepo, Field: ".serf/launch.toml",
			Message: fmt.Sprintf("read: %v", err),
		}}
	}
	hash, err := CanonicalHashTOML(data)
	if err != nil {
		return &RepoStatus{Path: repoPath, Trust: TrustUntrusted, Preview: string(data)}, Layer{}, []Diagnostic{{
			Layer: LayerRepo, Field: ".serf/launch.toml",
			Message: fmt.Sprintf("hash: %v", err),
		}}
	}
	meta, _ := loadMetaFS(fs, filepath.Join(stateRoot, "projects", project.ID, "meta.toml"))
	state := ComputeTrustState(hash, meta)

	status := &RepoStatus{Path: repoPath, Hash: hash, Trust: state}
	if state != TrustTrusted {
		status.Preview = string(data)
	}

	var layer Layer
	var diags []Diagnostic
	if state == TrustTrusted {
		returnLayer, returnDiags := decodeTrustedRepoLayer(cwd, data, func(data []byte, out interface{}) error {
			_, err := tomlDecode(data, out)
			return err
		})
		layer, diags = returnLayer, returnDiags
	}
	return status, layer, diags
}

func decodeTrustedRepoLayer(repoRoot string, data []byte, decode func([]byte, interface{}) error) (Layer, []Diagnostic) {
	var layer Layer
	if err := decode(data, &layer); err != nil {
		return Layer{}, []Diagnostic{{Layer: LayerRepo, Field: ".serf/launch.toml", Message: err.Error()}}
	}
	return validateAndExpandRepoLayer(repoRoot, layer)
}

// validateAndExpandRepoLayer rejects path entries that escape the repo
// root and expands every remaining path to an absolute path anchored on
// repoRoot. Returns the cleaned layer plus diagnostics for rejected
// entries.
func validateAndExpandRepoLayer(repoRoot string, in Layer) (Layer, []Diagnostic) {
	var diags []Diagnostic
	expand := func(field string, vals []string) []string {
		out := make([]string, 0, len(vals))
		for _, v := range vals {
			if err := ValidateRepoRelativePath(repoRoot, v); err != nil {
				diags = append(diags, Diagnostic{Layer: LayerRepo, Field: field, Message: err.Error()})
				continue
			}
			out = append(out, filepath.Clean(filepath.Join(repoRoot, v)))
		}
		return out
	}
	expandOne := func(field, value string) string {
		if value == "" {
			return ""
		}
		if err := ValidateRepoRelativePath(repoRoot, value); err != nil {
			diags = append(diags, Diagnostic{Layer: LayerRepo, Field: field, Message: err.Error()})
			return ""
		}
		return filepath.Clean(filepath.Join(repoRoot, value))
	}
	in.SkillsDirs = expand("skills_dirs", in.SkillsDirs)
	in.PluginDirs = expand("plugin_dirs", in.PluginDirs)
	in.MCPConfigs = expand("mcp_configs", in.MCPConfigs)
	in.SystemPromptFile = expandOne("system_prompt_file", in.SystemPromptFile)
	in.SystemPromptAppendFile = expandOne("system_prompt_append_file", in.SystemPromptAppendFile)
	in.SystemPromptAppend = expand("system_prompt_append", in.SystemPromptAppend)
	in.TraceFile = expandOne("trace_file", in.TraceFile)
	in.CPUProfile = expandOne("cpu_profile", in.CPUProfile)
	in.ExportATIFPath = expandOne("export_atif_path", in.ExportATIFPath)
	return in, diags
}

// validateAbsolutePaths rejects relative paths at the global/project
// layers, dropping rejected entries with a diagnostic.
func validateAbsolutePaths(layer LayerName, in Layer, diags *[]Diagnostic) Layer {
	check := func(field string, vals []string) []string {
		out := make([]string, 0, len(vals))
		for _, v := range vals {
			if err := ValidateAbsolutePath(v); err != nil {
				if diags != nil {
					*diags = append(*diags, Diagnostic{Layer: layer, Field: field, Message: err.Error()})
				}
				continue
			}
			out = append(out, v)
		}
		return out
	}
	checkOne := func(field, value string) string {
		if value == "" {
			return ""
		}
		if err := ValidateAbsolutePath(value); err != nil {
			if diags != nil {
				*diags = append(*diags, Diagnostic{Layer: layer, Field: field, Message: err.Error()})
			}
			return ""
		}
		return value
	}
	in.SkillsDirs = check("skills_dirs", in.SkillsDirs)
	in.PluginDirs = check("plugin_dirs", in.PluginDirs)
	in.MCPConfigs = check("mcp_configs", in.MCPConfigs)
	in.SystemPromptFile = checkOne("system_prompt_file", in.SystemPromptFile)
	in.SystemPromptAppendFile = checkOne("system_prompt_append_file", in.SystemPromptAppendFile)
	in.SystemPromptAppend = check("system_prompt_append", in.SystemPromptAppend)
	in.TraceFile = checkOne("trace_file", in.TraceFile)
	in.CPUProfile = checkOne("cpu_profile", in.CPUProfile)
	in.ExportATIFPath = checkOne("export_atif_path", in.ExportATIFPath)
	return in
}
