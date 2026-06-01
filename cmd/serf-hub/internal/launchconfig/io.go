package launchconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// LoadLayer reads a Layer from path. A missing file is not an error —
// it returns a zero Layer.
func LoadLayer(path string) (Layer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Layer{}, nil
		}
		return Layer{}, fmt.Errorf("launchconfig: read %s: %w", path, err)
	}
	var out Layer
	if _, err := tomlDecode(data, &out); err != nil {
		return Layer{}, fmt.Errorf("launchconfig: parse %s: %w", path, err)
	}
	return out, nil
}

// SaveLayer writes a Layer to path atomically: it writes to path.tmp,
// fsync's it, then renames over the target. Mode 0600.
func SaveLayer(path string, layer Layer) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("launchconfig: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("launchconfig: open %s: %w", tmp, err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(layer); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("launchconfig: encode %s: %w", tmp, err)
	}
	data := buf.String()
	if layer.ModelFallbacksSet && len(layer.ModelFallbacks) == 0 {
		data = "model_fallbacks = []\n" + data
	}
	if _, err := f.WriteString(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("launchconfig: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("launchconfig: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("launchconfig: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("launchconfig: rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// LoadMeta reads a Meta from path. Missing returns a zero value.
func LoadMeta(path string) (Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Meta{}, nil
		}
		return Meta{}, fmt.Errorf("launchconfig: read %s: %w", path, err)
	}
	var out Meta
	if _, err := toml.Decode(string(data), &out); err != nil {
		return Meta{}, fmt.Errorf("launchconfig: parse %s: %w", path, err)
	}
	return out, nil
}

// tomlDecode is the inverse of SaveLayer's encoder; exposed for use by
// callers (the resolver) that have already read raw bytes.
func tomlDecode(data []byte, out interface{}) (toml.MetaData, error) {
	meta, err := toml.Decode(string(data), out)
	if err != nil {
		return meta, err
	}
	if layer, ok := out.(*Layer); ok {
		layer.ModelFallbacksSet = meta.IsDefined("model_fallbacks")
	}
	return meta, nil
}

// SaveMeta writes a Meta to path atomically.
func SaveMeta(path string, meta Meta) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("launchconfig: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("launchconfig: open %s: %w", tmp, err)
	}
	if err := toml.NewEncoder(f).Encode(meta); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("launchconfig: encode %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("launchconfig: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("launchconfig: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("launchconfig: rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
