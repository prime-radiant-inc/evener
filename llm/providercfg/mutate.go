package providercfg

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/spf13/afero"
)

// WriteFile marshals cfg and atomically writes it to path (temp + rename),
// mode 0644. It creates parent directories as needed. Runtime API keys and
// credential headers are scrubbed, then each instance's authored fields are
// restored from the existing file. This preserves hand-authored literals or
// environment expressions across rewrites without persisting resolved values
// or introducing credentials the user did not write.
func WriteFile(path string, cfg Config) error {
	return writeFileFS(afero.NewOsFs(), path, cfg)
}

type persistedCredentialFields struct {
	APIKey            string
	CredentialHeaders map[string]string
}

// onDiskCredentialFields returns the authored credential-bearing fields from
// the existing file. It tolerates an absent or unparseable file and decodes the
// raw shape without validation so a restore cannot fail a rewrite.
func onDiskCredentialFields(fs afero.Fs, path string) map[string]persistedCredentialFields {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		return nil
	}
	var raw fileShape
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil
	}
	fields := make(map[string]persistedCredentialFields, len(raw.Instances))
	for name, inst := range raw.Instances {
		if inst.APIKey != "" || len(inst.CredentialHeaders) > 0 {
			fields[name] = persistedCredentialFields{
				APIKey:            inst.APIKey,
				CredentialHeaders: maps.Clone(inst.CredentialHeaders),
			}
		}
	}
	return fields
}

// writeFileFS is the filesystem seam beneath WriteFile: it performs the atomic
// temp+rename write through an injected afero.Fs. Production passes
// afero.NewOsFs(), whose methods delegate directly to os, so behavior is
// byte-identical to using os calls; tests and fuzzers inject an in-memory or
// sandboxed filesystem to exercise persistence without touching real disk.
func writeFileFS(fs afero.Fs, path string, cfg Config) error {
	return writeFileCodecFS(fs, path, cfg, Marshal)
}

// writeFileCodecFS exposes the serialization boundary for deterministic
// failure-path tests while production always passes Marshal.
func writeFileCodecFS(fs afero.Fs, path string, cfg Config, marshal func(Config) ([]byte, error)) error {
	disk := onDiskCredentialFields(fs, path)
	scrubbed := Config{Default: cfg.Default, Instances: append([]InstanceConfig(nil), cfg.Instances...)}
	for i := range scrubbed.Instances {
		authored := disk[scrubbed.Instances[i].Name]
		scrubbed.Instances[i].APIKey = authored.APIKey
		scrubbed.Instances[i].CredentialHeaders = maps.Clone(authored.CredentialHeaders)
	}
	cfg = scrubbed
	data, err := marshal(cfg)
	if err != nil {
		return fmt.Errorf("providers.toml: marshal: %w", err)
	}
	// Refuse to persist a config the next Load would reject (e.g. a hub edit
	// that moves an instance out of the compat family while it still carries
	// compat/models tables, or removing the last instance) — a loud write
	// failure now beats bricking every future startup.
	if _, err := Load(data); err != nil {
		return fmt.Errorf("providers.toml: refusing to write a config that would fail to load: %w", err)
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
