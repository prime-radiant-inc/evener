//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package skill

// platformAcquireSkillsLease cannot lock on this platform. Nothing is reaped
// there either: tryExclusiveLease refuses the no-op lease, so a reaper never
// holds the exclusive lease it needs and an unleased copy is left in place.
func platformAcquireSkillsLease(string, bool) (skillsLease, bool, error) {
	return noopSkillsLease{}, false, nil
}
