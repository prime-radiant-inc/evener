//go:build !unix && !windows

package skill

// platformAcquireSkillsLease cannot lock on this platform, so reaping falls back
// to age alone there.
func platformAcquireSkillsLease(string, bool) (skillsLease, bool, error) {
	return noopSkillsLease{}, false, nil
}

type noopSkillsLease struct{}

func (noopSkillsLease) Release() error { return nil }

func (noopSkillsLease) Valid() bool { return true }
