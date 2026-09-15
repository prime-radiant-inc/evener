//go:build windows

package skill

import (
	"errors"
	"os"
)

// defaultSkillsBaseRoot is where the private per-user cache base lives. Windows
// has no sandbox read confinement, and the user cache directory is protected by
// per-user ACLs, so the cache lives there rather than in a shared temp root
// where another account could create the name first. When that directory cannot
// be named it reports an error and the caller fails closed: a temp root cannot
// be verified, because the Windows ownership predicates cannot tell who created
// a directory.
func defaultSkillsBaseRoot() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil || cache == "" {
		return "", errors.New("user cache directory unavailable")
	}
	return cache, nil
}
