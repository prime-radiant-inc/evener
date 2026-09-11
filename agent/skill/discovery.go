package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/execenv"
)

// PluginSource identifies a manifest-selected plugin root, without loading its components.
type PluginSource struct{ Name, Dir string }

// DiscoverOptions supplies automatic and explicitly configured discovery roots.
// Empty automatic roots are omitted; missing automatic directories are normal.
type DiscoverOptions struct {
	HomeDir       string
	UserSkillsDir string
	ExtraDirs     []string
	Plugins       []PluginSource
}

// Catalog retains all known winners, including unavailable and unadvertised skills.
type Catalog struct {
	Entries     map[string]Descriptor
	Diagnostics []Diagnostic
}

type ResolutionError struct {
	Kind       string
	Name       string
	Candidates []string
}

func (e *ResolutionError) Error() string {
	if e.Kind == "ambiguous" {
		return fmt.Sprintf("ambiguous skill %q: %s", e.Name, strings.Join(e.Candidates, ", "))
	}
	return fmt.Sprintf("unknown skill %q", e.Name)
}

func (c Catalog) ResolveExact(name string) (Descriptor, error) {
	if d, ok := c.Entries[name]; ok {
		return d, nil
	}
	return Descriptor{}, &ResolutionError{Kind: "unknown", Name: name}
}

// ResolveSkillContent loads the selected source for existing role preloads.
// Unknown or ambiguous names retain their existing empty-body behavior.
func (c Catalog) ResolveSkillContent(name string) (string, error) {
	d, err := c.Resolve(name)
	if err != nil {
		return "", nil
	}
	loaded, _, err := Load(d)
	if err != nil {
		return "", err
	}
	return loaded.Body, nil
}

func (c Catalog) Resolve(name string) (Descriptor, error) {
	if d, ok := c.Entries[name]; ok {
		return d, nil
	}
	var candidates []string
	if !strings.Contains(name, ":") {
		for key := range c.Entries {
			if strings.HasSuffix(key, ":"+name) {
				candidates = append(candidates, key)
			}
		}
	}
	slices.Sort(candidates)
	if len(candidates) == 1 {
		return c.Entries[candidates[0]], nil
	}
	if len(candidates) > 1 {
		return Descriptor{}, &ResolutionError{Kind: "ambiguous", Name: name, Candidates: candidates}
	}
	return Descriptor{}, &ResolutionError{Kind: "unknown", Name: name}
}

func (c Catalog) ModelEntries() []Descriptor {
	return c.advertisedEntries(false)
}
func (c Catalog) UserEntries() []Descriptor {
	return c.advertisedEntries(true)
}

// InspectionEntries returns all metadata in canonical order, without source
// paths or references through which an inspector could mutate the catalog.
func (c Catalog) InspectionEntries() []Descriptor {
	names := make([]string, 0, len(c.Entries))
	for name := range c.Entries {
		names = append(names, name)
	}
	slices.Sort(names)
	result := make([]Descriptor, 0, len(names))
	for _, name := range names {
		d := c.Entries[name]
		d.Meta.Dir, d.Meta.SkillFile = "", ""
		d.Meta.AllowedTools = append([]string(nil), d.Meta.AllowedTools...)
		d.Meta.Metadata = cloneMetadata(d.Meta.Metadata)
		result = append(result, d)
	}
	return result
}

func (c Catalog) advertisedEntries(user bool) []Descriptor {
	names := make([]string, 0, len(c.Entries))
	for name, d := range c.Entries {
		allowed := !d.Controls.DisableModelInvocation
		if user {
			allowed = d.Controls.UserInvocable
		}
		if !d.Unavailable && allowed {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	result := make([]Descriptor, 0, len(names))
	for _, name := range names {
		result = append(result, c.Entries[name])
	}
	return result
}

// Discover scans in increasing precedence, preserving diagnostics and reserving
// known names even when the winning metadata is invalid. It only reads text.
func Discover(env execenv.ExecutionEnvironment, opts DiscoverOptions) Catalog {
	c := Catalog{Entries: make(map[string]Descriptor)}
	insert := func(d Descriptor) {
		if d.CatalogName == "" {
			return
		}
		if previous, ok := c.Entries[d.CatalogName]; ok {
			c.Diagnostics = append(c.Diagnostics, Diagnostic{Category: "collision", Name: d.CatalogName, Source: d.Meta.SkillFile, OtherSource: previous.Meta.SkillFile, Message: "higher-precedence skill replaces earlier source"})
		}
		c.Entries[d.CatalogName] = d
	}
	scan := func(dir, pluginName string, optional bool) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		absolute, err := filepath.Abs(dir)
		if err != nil {
			c.Diagnostics = append(c.Diagnostics, Diagnostic{Category: "unreadable_source", Source: dir, Message: err.Error()})
			return
		}
		entries, err := os.ReadDir(absolute)
		if err != nil {
			if !optional || !os.IsNotExist(err) {
				c.Diagnostics = append(c.Diagnostics, Diagnostic{Category: "unreadable_source", Source: absolute, Message: err.Error()})
			}
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			source := filepath.Join(absolute, entry.Name(), "SKILL.md")
			data, err := os.ReadFile(source)
			if err != nil {
				// A directory without a skill file is not a skill candidate.
				if !os.IsNotExist(err) {
					c.Diagnostics = append(c.Diagnostics, Diagnostic{Category: "unreadable_source", Source: source, Message: err.Error()})
				}
				continue
			}
			d, diagnostics, _ := Parse(data, source)
			if pluginName != "" && d.CatalogName != "" {
				d.CatalogName = pluginName + ":" + d.CatalogName
				for i := range diagnostics {
					if diagnostics[i].Name != "" {
						diagnostics[i].Name = d.CatalogName
					}
				}
				if !IsSlashAddressableName(d.CatalogName) {
					diagnostics = append(diagnostics, Diagnostic{Category: "invalid_metadata", Name: d.CatalogName, Source: source, Field: "name", Message: "qualified name is not slash-addressable"})
					d.Unavailable = true
				}
			}
			c.Diagnostics = append(c.Diagnostics, diagnostics...)
			insert(d)
		}
	}
	if dir, err := EmbeddedSkillsDir(); err != nil {
		c.Diagnostics = append(c.Diagnostics, Diagnostic{Category: "unreadable_source", Source: "embedded", Message: err.Error()})
	} else {
		scan(dir, "", false)
	}
	if opts.HomeDir != "" {
		scan(filepath.Join(opts.HomeDir, ".agents", "skills"), "", true)
	}
	scan(opts.UserSkillsDir, "", true)
	for _, dir := range projectSkillDirs(env) {
		scan(dir, "", true)
	}
	for _, dir := range opts.ExtraDirs {
		scan(dir, "", false)
	}
	for _, p := range opts.Plugins {
		scan(filepath.Join(p.Dir, "skills"), p.Name, true)
	}
	return c
}

func projectSkillDirs(env execenv.ExecutionEnvironment) []string {
	if env == nil {
		return nil
	}
	cwd := strings.TrimSpace(env.WorkingDirectory())
	if cwd == "" {
		return nil
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	root := cwd
	if gr := execenv.GitRootOrEmpty(env, cwd); gr != "" {
		root = gr
	}
	var dirs []string
	for _, dir := range execenv.DirsFromRootToCwd(root, cwd) {
		dirs = append(dirs, filepath.Join(dir, ".agents", "skills"), filepath.Join(dir, "skills"))
	}
	return dirs
}
