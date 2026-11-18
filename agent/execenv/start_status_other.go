//go:build !unix

package execenv

// startRefusedByKernel reports whether err is a launcher refusing a program it
// resolved. No syscall refusal set applies here: Windows is not a shipping
// target (.goreleaser.yml builds linux and darwin), and it keeps this file only
// so the cross-vet compiles. A Windows start failure therefore stays a plain
// error rather than borrowing a POSIX shell status that would not describe it.
func startRefusedByKernel(error) bool { return false }
