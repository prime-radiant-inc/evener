package registry

import (
	"bytes"
	_ "embed"
)

//go:embed data/providers_overlay.toml
var embeddedOverlay []byte

// EmbeddedOverlay returns an owned copy of the curated overlay compiled into
// the binary (spec §5 layer 3, §6.2).
func EmbeddedOverlay() []byte { return bytes.Clone(embeddedOverlay) }

// ParseOverlay parses a curated overlay (spec §6.2): providers.toml's schema
// plus the curated-only keys implicit, name, doc, default_order, and
// [transports.*].
func ParseOverlay(data []byte) (*Layer, error) {
	return parseLayer(data, LayerOverlay, true)
}
