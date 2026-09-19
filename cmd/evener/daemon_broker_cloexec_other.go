//go:build !unix

package main

import "errors"

// Inherited descriptor launch relies on exec.Cmd.ExtraFiles, which is not
// supported on these platforms. Refuse the broker rather than leave an
// authority-bearing handle inheritable by descendants.
func sealInheritedDaemonBrokerDescriptors(_, _ int) error {
	return errors.New("inherited artifact broker descriptors are unsupported on this platform")
}
