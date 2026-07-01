package launchconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/spf13/afero"
)

// LoadLayer reads a Layer from path. A missing file is not an error —
// it returns a zero Layer.
func LoadLayer(path string) (Layer, error) {
	return loadLayerFS(afero.NewOsFs(), path)
}

// loadLayerFS is the filesystem seam beneath LoadLayer: it reads a Layer over
// an injected afero.Fs. Production passes afero.NewOsFs(), whose calls forward
// straight to the os package, so behavior is identical to a direct os.ReadFile;
// tests and fuzzers inject an in-memory or sandboxed filesystem.
func loadLayerFS(fs afero.Fs, path string) (Layer, error) {
	data, err := afero.ReadFile(fs, path)
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
	return saveLayerFS(afero.NewOsFs(), path, layer)
}

// saveLayerFS is the filesystem seam beneath SaveLayer. See loadLayerFS.
func saveLayerFS(fs afero.Fs, path string, layer Layer) error {
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("launchconfig: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	f, err := fs.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("launchconfig: open %s: %w", tmp, err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(layer); err != nil {
		_ = f.Close()
		_ = fs.Remove(tmp)
		return fmt.Errorf("launchconfig: encode %s: %w", tmp, err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		_ = fs.Remove(tmp)
		return fmt.Errorf("launchconfig: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = fs.Remove(tmp)
		return fmt.Errorf("launchconfig: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("launchconfig: close %s: %w", tmp, err)
	}
	if err := fs.Rename(tmp, path); err != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("launchconfig: rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// LoadMeta reads a Meta from path. Missing returns a zero value.
func LoadMeta(path string) (Meta, error) {
	return loadMetaFS(afero.NewOsFs(), path)
}

// loadMetaFS is the filesystem seam beneath LoadMeta. See loadLayerFS.
func loadMetaFS(fs afero.Fs, path string) (Meta, error) {
	data, err := afero.ReadFile(fs, path)
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
	return toml.Decode(string(data), out)
}

// SaveMeta writes a Meta to path atomically.
func SaveMeta(path string, meta Meta) error {
	return saveMetaFS(afero.NewOsFs(), path, meta)
}

// saveMetaFS is the filesystem seam beneath SaveMeta. See loadLayerFS.
func saveMetaFS(fs afero.Fs, path string, meta Meta) error {
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("launchconfig: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	f, err := fs.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("launchconfig: open %s: %w", tmp, err)
	}
	if err := toml.NewEncoder(f).Encode(meta); err != nil {
		_ = f.Close()
		_ = fs.Remove(tmp)
		return fmt.Errorf("launchconfig: encode %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = fs.Remove(tmp)
		return fmt.Errorf("launchconfig: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("launchconfig: close %s: %w", tmp, err)
	}
	if err := fs.Rename(tmp, path); err != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("launchconfig: rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
