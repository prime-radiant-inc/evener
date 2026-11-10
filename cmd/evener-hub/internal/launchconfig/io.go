package launchconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/spf13/afero"

	"primeradiant.com/evener/execsupport/fsatomic"
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

// SaveLayer writes a Layer to path durably and atomically (fsatomic.Write).
// Mode 0600.
func SaveLayer(path string, layer Layer) error {
	return writeAtomic(path, func(w io.Writer) error {
		return toml.NewEncoder(w).Encode(layer)
	})
}

func writeAtomic(path string, encode func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("launchconfig: mkdir %s: %w", filepath.Dir(path), err)
	}
	var buf bytes.Buffer
	if err := encode(&buf); err != nil {
		return fmt.Errorf("launchconfig: encode %s: %w", path, err)
	}
	if err := fsatomic.Write(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("launchconfig: write %s: %w", path, err)
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
func tomlDecode(data []byte, out any) (toml.MetaData, error) {
	return toml.Decode(string(data), out)
}

// SaveMeta writes a Meta to path durably and atomically. Mode 0600.
func SaveMeta(path string, meta Meta) error {
	return writeAtomic(path, func(w io.Writer) error {
		return toml.NewEncoder(w).Encode(meta)
	})
}
