//go:build windows

package skill

import "os"

// defaultSkillsBaseRoot is where the private per-user cache base lives. Windows
// has no sandbox read confinement, and the user cache directory is protected by
// per-user ACLs, so the cache lives there rather than in a shared temp root
// where another account could create the name first.
func defaultSkillsBaseRoot() string {
	cache, err := os.UserCacheDir()
	if err != nil || cache == "" {
		// An empty root sends the caller to its randomized private base. The temp
		// root is not a substitute: the name there is predictable, and the Windows
		// ownership predicates cannot tell whether another account created it.
		return ""
	}
	return cache
}
