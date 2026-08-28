package provider

import (
	"primeradiant.com/evener/llm/registry"
)

// Resolve builds the profile for an instance/model reference ("work/glm-5",
// "anthropic/claude-opus-5", or a bare model on the default instance). It is
// network-free: every fact comes from the registry the caller supplies.
func Resolve(r *registry.Registry, ref string) (*Profile, error) {
	res, err := r.Resolve(ref)
	if err != nil {
		return nil, err
	}
	return FromResolved(res, r), nil
}
