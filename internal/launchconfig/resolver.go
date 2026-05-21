package launchconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Resolve loads and merges every layer for the given cwd, applying the
// per-launch override on top. stateRoot is typically ~/.serf. The repo
// layer is honored only when its trust state is "trusted".
func Resolve(stateRoot, cwd string, overrides Layer) (Resolved, error) {
	paths := PathsFor(stateRoot, cwd)
	layers := map[LayerName]Layer{}

	g, err := LoadLayer(paths.Global)
	if err != nil {
		return Resolved{}, fmt.Errorf("global: %w", err)
	}
	g = validateAbsolutePaths(LayerGlobal, g, nil)
	layers[LayerGlobal] = g

	// In-repo: load + hash + trust check.
	repoStatus, repoLayer, repoDiags := loadRepoLayer(cwd, stateRoot)
	if repoStatus != nil && repoStatus.Trust == TrustTrusted {
		layers[LayerRepo] = repoLayer
	}

	p, projectDiags, err := LoadProjectLayer(paths)
	if err != nil {
		return Resolved{}, fmt.Errorf("project: %w", err)
	}
	p = validateAbsolutePaths(LayerProject, p, nil)
	layers[LayerProject] = p

	layers[LayerLaunch] = overrides

	resolved, _ := mergeLayers(layers)
	resolved.Repo = repoStatus
	resolved.Diagnostics = append(resolved.Diagnostics, repoDiags...)
	resolved.Diagnostics = append(resolved.Diagnostics, projectDiags...)
	return resolved, nil
}

// LoadProjectLayer reads the local project layer. The canonical path is
// <cwd>/.serf/launch.local.toml; the old hub-state path remains a read-only
// fallback so existing project defaults continue to apply until the layer is
// saved again.
func LoadProjectLayer(paths Paths) (Layer, []Diagnostic, error) {
	if _, err := os.Stat(paths.Project); err == nil {
		layer, err := LoadLayer(paths.Project)
		return layer, nil, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return Layer{}, nil, err
	}

	if _, err := os.Stat(paths.LegacyProject); err == nil {
		layer, err := LoadLayer(paths.LegacyProject)
		if err != nil {
			return Layer{}, nil, err
		}
		return layer, []Diagnostic{{
			Layer:   LayerProject,
			Field:   "launch.local.toml",
			Message: fmt.Sprintf("using legacy project launch config at %s; save the project layer to migrate to %s", paths.LegacyProject, paths.Project),
		}}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Layer{}, nil, err
	}

	return Layer{}, nil, nil
}

func loadRepoLayer(cwd, stateRoot string) (*RepoStatus, Layer, []Diagnostic) {
	repoPath := filepath.Join(cwd, ".serf", "launch.toml")
	data, err := os.ReadFile(repoPath)
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
	meta, _ := LoadMeta(filepath.Join(stateRoot, "projects", ProjectID(cwd), "meta.toml"))
	state := ComputeTrustState(hash, meta)

	status := &RepoStatus{Path: repoPath, Hash: hash, Trust: state}
	if state != TrustTrusted {
		status.Preview = string(data)
	}

	var layer Layer
	var diags []Diagnostic
	if state == TrustTrusted {
		if _, err := tomlDecode(data, &layer); err != nil {
			diags = append(diags, Diagnostic{Layer: LayerRepo, Field: ".serf/launch.toml", Message: err.Error()})
			return status, Layer{}, diags
		}
		layer, diags = validateAndExpandRepoLayer(cwd, layer)
	}
	return status, layer, diags
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
	in.SkillsDirs = expand("skills_dirs", in.SkillsDirs)
	in.PluginDirs = expand("plugin_dirs", in.PluginDirs)
	in.MCPConfigs = expand("mcp_configs", in.MCPConfigs)
	in.SystemPromptAppend = expand("system_prompt_append", in.SystemPromptAppend)
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
	in.SkillsDirs = check("skills_dirs", in.SkillsDirs)
	in.PluginDirs = check("plugin_dirs", in.PluginDirs)
	in.MCPConfigs = check("mcp_configs", in.MCPConfigs)
	in.SystemPromptAppend = check("system_prompt_append", in.SystemPromptAppend)
	return in
}
