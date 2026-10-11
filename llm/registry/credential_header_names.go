package registry

import (
	"maps"
	"slices"
)

// credentialHeaderNames names the credential headers a launch through
// transport t would actually send, without expanding any value: the hub's
// configuration-revision digest covers the names — which headers exist
// decides whether a stored key is sent at all — but the values are wire
// material, and a command-bearing one must not run on a read path (spec
// §10.1). A header counts as present under the same rules
// expandCredentialHeaders applies: a raw-empty value is the authored
// removal, the auth slot follows the presence judgment (a command counts
// as present without running), and an environment-only header drops when
// it expands to nothing or nothing but an auth scheme word.
func (r *Registry) credentialHeaderNames(rec *record, t Transport) []string {
	auth := r.authorizationMode(rec, t, true)
	authPresent := false
	if auth.present {
		switch {
		case auth.commandBorne:
			authPresent = true
		case len(auth.unresolved) == 0 && auth.expanded != "" && !auth.noMaterial:
			authPresent = true
		}
	}
	var names []string
	for _, k := range slices.Sorted(maps.Keys(rec.head.CredentialHeaders)) {
		v := rec.head.CredentialHeaders[k]
		if v == "" {
			continue
		}
		if auth.present && k == auth.key {
			if authPresent {
				names = append(names, k)
			}
			continue
		}
		if hasCommandMaterial(v) {
			names = append(names, k)
			continue
		}
		if e, missing := expandEnv(v, r.env); len(missing) == 0 && e != "" && !r.schemeWordDefault(v) {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	return names
}
