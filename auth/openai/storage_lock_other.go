//go:build !linux && !darwin

package openai

// acquireAuthStoreLock is the no-op where no advisory lock primitive is wired
// up. Evener ships Linux and Darwin only; this file exists so the tagged
// GOOS=windows cross-vet compiles. Without a kernel lock the cross-process
// ordering the other build provides does not exist here, and the save paths
// behave as they did before it.
func acquireAuthStoreLock(stateDir string) (func(), error) {
	return func() {}, nil
}
