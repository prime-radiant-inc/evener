package registry

// ParseConfig parses a providers.toml (spec §10). Curated-only keys and any
// unknown key are errors; a pre-registry file is reported as ErrOldSchema.
func ParseConfig(data []byte) (*Layer, error) {
	return parseLayer(data, LayerConfig, false)
}
