package registry

// ParseOverlay parses the curated overlay (spec §6.2): providers.toml's
// schema plus the curated-only keys implicit, name, doc, default_order, and
// [transports.*]. The embedded overlay is added in Task 5.
func ParseOverlay(data []byte) (*Layer, error) {
	return parseLayer(data, LayerOverlay, true)
}
