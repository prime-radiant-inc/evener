package main

import (
	"errors"
	"fmt"
	"os"

	"primeradiant.com/evener/internal/interactiveartifacts"
)

func openInheritedDaemonBroker(readFD, writeFD int) (*interactiveartifacts.DaemonBroker, error) {
	if readFD < 3 || writeFD < 3 || readFD == writeFD {
		return nil, errors.New("artifact broker descriptors are invalid")
	}
	reader := os.NewFile(uintptr(readFD), "artifact-broker-read")
	writer := os.NewFile(uintptr(writeFD), "artifact-broker-write")
	if reader == nil || writer == nil {
		if reader != nil {
			_ = reader.Close()
		}
		if writer != nil {
			_ = writer.Close()
		}
		return nil, errors.New("artifact broker descriptors are unavailable")
	}

	// These descriptors carry Hub authority. Seal inheritance before any plugin,
	// tool, shell, or session construction can start a descendant process.
	if err := sealInheritedDaemonBrokerDescriptors(readFD, writeFD); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	launchID, err := interactiveartifacts.ReadLaunchCorrelation(reader)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, fmt.Errorf("read artifact broker launch correlation: %w", err)
	}
	return interactiveartifacts.NewDaemonBroker(interactiveartifacts.DaemonBrokerConfig{
		Transport: interactiveartifacts.NewPrivateBrokerTransport(reader, writer),
		LaunchID:  launchID,
	}), nil
}
