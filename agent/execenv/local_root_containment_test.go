package execenv

import "testing"

// A filesystem-root workspace contains every absolute path. The containment
// check once built the prefix "//" and so rejected every child of "/".
func TestEnsureUnderRootFilesystemRootContainsChildren(t *testing.T) {
	env := NewLocalExecutionEnvironment("/")
	for _, path := range []string{"/", "/etc", "/etc/hosts"} {
		if err := env.ensureUnderRoot(path); err != nil {
			t.Errorf("ensureUnderRoot(%q) under root %q = %v, want nil", path, env.RootDir, err)
		}
	}
}
