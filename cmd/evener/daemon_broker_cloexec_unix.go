//go:build unix

package main

import "syscall"

func sealInheritedDaemonBrokerDescriptors(readFD, writeFD int) error {
	syscall.CloseOnExec(readFD)
	syscall.CloseOnExec(writeFD)
	return nil
}
