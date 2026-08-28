//go:build linux || darwin

package execenv

// pinScratchSandboxRoot opens and caches the scratch root before the first
// operation. The descriptor remains anchored until environment teardown, so a
// later replacement of the root path cannot redirect a file-tool operation.
func pinScratchSandboxRoot(sfs *sandboxFS, root string) error {
	_, err := sfs.rootFd(root)
	return err
}
