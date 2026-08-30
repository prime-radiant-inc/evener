package registry

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

// MarshalConfig renders a user layer as providers.toml (spec §10). Only the
// keys the Layer sets are written — nil pointers, empty maps, empty
// strings, and nil slices are absent — so ParseConfig(MarshalConfig(l))
// yields l and no default or resolved value ever lands on disk. An explicit
// empty api_key_env list is kept: `api_key_env = []` is how a Codex-style
// entry says "no key variable" (spec §6.2).
func MarshalConfig(l *Layer) ([]byte, error) {
	doc := map[string]any{}
	if l.Default != "" {
		doc["default"] = l.Default
	}
	if len(l.TopGlobs) > 0 {
		doc["models"] = modelTables(l.TopGlobs)
	}
	if len(l.Providers) > 0 {
		providers := make(map[string]any, len(l.Providers))
		for name, p := range l.Providers {
			providers[name] = providerTable(p)
		}
		doc["providers"] = providers
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return nil, fmt.Errorf("marshal providers.toml: %w", err)
	}
	return buf.Bytes(), nil
}

func providerTable(p Provider) map[string]any {
	t := map[string]any{}
	setString(t, "base", p.Base)
	if p.InheritModels != nil {
		t["inherit_models"] = *p.InheritModels
	}
	setString(t, "protocol", p.Protocol)
	setString(t, "surface", p.Surface)
	setString(t, "family", p.Family)
	setString(t, "api_key", p.APIKey)
	if p.APIKeyEnv != nil {
		t["api_key_env"] = p.APIKeyEnv
	}
	setStringMap(t, "headers", p.Headers)
	setStringMap(t, "credential_headers", p.CredentialHeaders)
	setString(t, "default_model", p.DefaultModel)
	setString(t, "cheap_model", p.CheapModel)
	transportInto(t, p.Transport)
	capsInto(t, p.Caps)
	if len(p.Models) > 0 {
		t["models"] = modelTables(p.Models)
	}
	return t
}

func modelTables(rows map[string]Model) map[string]any {
	out := make(map[string]any, len(rows))
	for id, m := range rows {
		t := map[string]any{}
		setString(t, "alias_of", m.AliasOf)
		setString(t, "wire_id", m.WireID)
		setString(t, "family", m.Family)
		setString(t, "protocol", m.Protocol)
		setString(t, "surface", m.Surface)
		setStringMap(t, "headers", m.Headers)
		if m.Transport != nil {
			transportInto(t, *m.Transport)
		}
		capsInto(t, m.Caps)
		out[id] = t
	}
	return out
}

func transportInto(t map[string]any, tr Transport) {
	setString(t, "transport", tr.Preset)
	setString(t, "auth", tr.Auth)
	setString(t, "auth_header", tr.AuthHeader)
	setString(t, "base_url", tr.BaseURL)
	setString(t, "host_rule", tr.HostRule)
	setString(t, "endpoint", tr.Endpoint)
	setString(t, "stream_endpoint", tr.StreamEndpoint)
	setString(t, "models_endpoint", tr.ModelsEndpoint)
	setString(t, "count_tokens_endpoint", tr.CountTokensEndpoint)
	setStringMap(t, "vars", tr.Vars)
	setStringMap(t, "vars_env", tr.VarsEnv)
	if len(tr.Body) > 0 {
		t["body"] = tr.Body
	}
}

// capsInto writes every set cap under its toml tag: non-nil pointers are
// dereferenced, non-empty slices and maps are copied, Cost becomes a table.
func capsInto(t map[string]any, c Caps) {
	rv := reflect.ValueOf(c)
	rt := rv.Type()
	for i := range rt.NumField() {
		key, _, _ := strings.Cut(rt.Field(i).Tag.Get("toml"), ",")
		f := rv.Field(i)
		switch f.Kind() {
		case reflect.Pointer:
			if f.IsNil() {
				continue
			}
			if cost, ok := reflect.TypeAssert[*Cost](f); ok {
				t[key] = costTable(*cost)
			} else {
				t[key] = f.Elem().Interface()
			}
		case reflect.Slice, reflect.Map:
			if f.Len() > 0 {
				t[key] = f.Interface()
			}
		}
	}
}

func costTable(c Cost) map[string]any {
	t := map[string]any{"input": c.Input, "output": c.Output, "cache_read": c.CacheRead, "cache_write": c.CacheWrite}
	if len(c.Tiers) > 0 {
		tiers := make([]map[string]any, 0, len(c.Tiers))
		for _, tier := range c.Tiers {
			tiers = append(tiers, map[string]any{"input_tokens_above": tier.InputTokensAbove, "input": tier.Input, "output": tier.Output, "cache_read": tier.CacheRead, "cache_write": tier.CacheWrite})
		}
		t["tiers"] = tiers
	}
	return t
}

func setString(t map[string]any, key, v string) {
	if v != "" {
		t[key] = v
	}
}

// ValidInstanceName is spec §10's instance-name rule (lowercase, no slash),
// the same predicate the parser applies, for callers that write entries.
func ValidInstanceName(name string) bool { return validProviderName(name) }

func setStringMap(t map[string]any, key string, m map[string]string) {
	if len(m) > 0 {
		t[key] = m
	}
}

// ReadConfigFile reads a providers.toml for editing. An absent file is an
// empty layer (exists = false); a parse error, including ErrOldSchema,
// propagates so no writer ever rewrites a file it could not read.
func ReadConfigFile(path string) (*Layer, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Layer{Tag: LayerConfig, Transports: map[string]Transport{}, TopGlobs: map[string]Model{}, Providers: map[string]Provider{}}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	l, err := ParseConfig(data)
	if err != nil {
		return nil, true, fmt.Errorf("%s: %w", path, err)
	}
	return l, true, nil
}

// WriteConfigFile marshals l and writes it atomically (temp + rename, mode
// 0644, parent directories created). It writes exactly what l holds: the
// caller decides what a user authored (spec §10 "WriteFile keeps today's
// scrub-and-restore" is satisfied by never putting a resolved credential
// into the Layer in the first place).
func WriteConfigFile(path string, l *Layer) error {
	data, err := MarshalConfig(l)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("providers.toml: mkdir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("providers.toml: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("providers.toml: rename: %w", err)
	}
	return nil
}
