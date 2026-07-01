package providercfg

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/spf13/afero"
)

// WriteFile marshals cfg and atomically writes it to path (temp + rename),
// mode 0644. It creates parent directories as needed. It never writes api_key
// values even if cfg contains them.
func WriteFile(path string, cfg Config) error {
	return writeFileFS(afero.NewOsFs(), path, cfg)
}

// writeFileFS is the filesystem seam beneath WriteFile: it performs the atomic
// temp+rename write through an injected afero.Fs. Production passes
// afero.NewOsFs(), whose methods delegate directly to os, so behavior is
// byte-identical to using os calls; tests and fuzzers inject an in-memory or
// sandboxed filesystem to exercise persistence without touching real disk.
func writeFileFS(fs afero.Fs, path string, cfg Config) error {
	data, err := Marshal(cfg)
	if err != nil {
		return fmt.Errorf("providers.toml: marshal: %w", err)
	}

	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("providers.toml: mkdir: %w", err)
	}

	tmp, err := afero.TempFile(fs, filepath.Dir(path), ".providers-*.toml.tmp")
	if err != nil {
		return fmt.Errorf("providers.toml: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup of the abandoned temp file on an error path; the
	// caller already receives the real failure, so these errors are ignored.
	cleanup := func() { _ = tmp.Close(); _ = fs.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("providers.toml: write temp file: %w", err)
	}
	if err := fs.Chmod(tmpName, 0o644); err != nil {
		cleanup()
		return fmt.Errorf("providers.toml: chmod: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("providers.toml: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = fs.Remove(tmpName)
		return fmt.Errorf("providers.toml: close: %w", err)
	}
	if err := fs.Rename(tmpName, path); err != nil {
		_ = fs.Remove(tmpName)
		return fmt.Errorf("providers.toml: rename: %w", err)
	}

	return nil
}

// Upsert returns a new Config with inst inserted or replacing the existing
// instance with the same Name. The returned Instances slice is sorted by Name.
// The receiver is never modified.
func (c Config) Upsert(inst InstanceConfig) Config {
	out := make([]InstanceConfig, 0, len(c.Instances)+1)
	replaced := false
	for _, existing := range c.Instances {
		if existing.Name == inst.Name {
			out = append(out, inst)
			replaced = true
		} else {
			out = append(out, existing)
		}
	}
	if !replaced {
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return Config{Default: c.Default, Instances: out}
}

// RemoveInstance returns a new Config with the instance named name removed.
// If no instance has that name, the returned Config is equivalent to the receiver.
// The receiver is never modified.
func (c Config) RemoveInstance(name string) Config {
	out := make([]InstanceConfig, 0, len(c.Instances))
	for _, inst := range c.Instances {
		if inst.Name != name {
			out = append(out, inst)
		}
	}
	return Config{Default: c.Default, Instances: out}
}

// WithDefault returns a new Config with Default set to name.
// The receiver is never modified.
func (c Config) WithDefault(name string) Config {
	return Config{Default: name, Instances: c.Instances}
}

// ValidateInstanceName reports whether name is a valid instance name:
// non-empty, all-lowercase, no '/'. It does not check uniqueness.
func ValidateInstanceName(name string) error {
	if name == "" {
		return errors.New("instance name must not be empty")
	}
	for _, r := range name {
		if unicode.IsUpper(r) {
			return fmt.Errorf("instance name %q must be lowercase", name)
		}
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("instance name %q must not contain '/'", name)
	}
	return nil
}

// ValidateAPIStyle reports whether style is valid for typ. An empty style is
// always valid. A non-empty style is only valid for typ "openai" and must be
// StyleResponses, StyleChatCompletions, or StyleAuto.
func ValidateAPIStyle(typ Type, style APIStyle) error {
	if style == "" {
		return nil
	}
	if typ != "openai" {
		return fmt.Errorf("api_style is only valid for type \"openai\", not %q", typ)
	}
	if style != StyleResponses && style != StyleChatCompletions && style != StyleAuto {
		return fmt.Errorf("unknown api_style %q (must be %q, %q, or %q)", style, StyleResponses, StyleChatCompletions, StyleAuto)
	}
	return nil
}

// ValidateType reports whether typ is a known provider type. Creating an
// instance with an unknown type would write a providers.toml that fails the
// next Load, so callers validate up front.
func ValidateType(typ Type) error {
	if !knownTypes[typ] {
		return fmt.Errorf("unknown provider type %q", typ)
	}
	return nil
}
