package sandbox

import "strings"

// IsSecretEnvName reports whether an environment variable NAME marks it as a
// credential that must not survive into a spawned process. It is the single deny
// predicate shared by every spawn surface (execenv shell/exec, plugin command
// hooks, stdio MCP servers), matching *API_KEY* / *SECRET* / *TOKEN* /
// *PASSWORD* / *CREDENTIAL* case-insensitively so a spawned command never
// inherits serf's provider key or other ambient secrets.
func IsSecretEnvName(name string) bool {
	u := strings.ToUpper(name)
	return strings.Contains(u, "API_KEY") ||
		strings.Contains(u, "SECRET") ||
		strings.Contains(u, "TOKEN") ||
		strings.Contains(u, "PASSWORD") ||
		strings.Contains(u, "CREDENTIAL")
}

// ScrubSecretEnv returns a copy of env ("NAME=VALUE" entries) with every
// credential-named variable removed (IsSecretEnvName). Entries without an '='
// are passed through unchanged. It never mutates its input.
func ScrubSecretEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if ok && IsSecretEnvName(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
