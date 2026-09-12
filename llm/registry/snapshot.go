package registry

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

//go:embed data/models.dev.json.gz data/models.dev.meta.json
var embeddedFS embed.FS

// Meta records when and with which ETag a models.dev snapshot was fetched
// (spec §5, §6.4). The runtime cache carries the same shape.
type Meta struct {
	FetchedAt time.Time `json:"fetched_at"`
	Etag      string    `json:"etag"`
	Source    string    `json:"source"`
}

// ParseMeta decodes a models.dev.meta.json document.
func ParseMeta(data []byte) (Meta, error) {
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("snapshot meta: %w", err)
	}
	return m, nil
}

// EmbeddedSnapshot returns the raw upstream JSON compiled into the binary and
// its fetch metadata (spec §5 layer 1).
func EmbeddedSnapshot() ([]byte, Meta, error) {
	gz, err := embeddedFS.ReadFile("data/models.dev.json.gz")
	if err != nil {
		return nil, Meta{}, err
	}
	raw, err := gunzip(gz)
	if err != nil {
		return nil, Meta{}, fmt.Errorf("embedded snapshot: %w", err)
	}
	metaRaw, err := embeddedFS.ReadFile("data/models.dev.meta.json")
	if err != nil {
		return nil, Meta{}, err
	}
	meta, err := ParseMeta(metaRaw)
	if err != nil {
		return nil, Meta{}, err
	}
	return raw, meta, nil
}

func gunzip(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}
