package agent

import (
	"embed"
	"os"
	"path/filepath"
)

// SectionSource provides read access to a directory of section files.
type SectionSource interface {
	ReadFile(name string) ([]byte, bool)
}

// diskSource reads section files from a filesystem directory.
type diskSource struct {
	dir string
}

func (d diskSource) ReadFile(name string) ([]byte, bool) {
	if d.dir == "" {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(d.dir, name))
	if err != nil {
		return nil, false
	}
	return data, true
}

// embedSource reads section files from an embedded filesystem.
type embedSource struct {
	fs     embed.FS
	prefix string // e.g. "prompts/sections/"
}

func (e embedSource) ReadFile(name string) ([]byte, bool) {
	data, err := e.fs.ReadFile(e.prefix + name)
	if err != nil {
		return nil, false
	}
	return data, true
}
