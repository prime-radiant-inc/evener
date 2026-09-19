package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/interactiveartifacts"
	"primeradiant.com/evener/rendezvous"
)

type daemonBrokerLaunchConfig struct {
	Authority *interactiveartifacts.HostAuthority
	ProjectID string
	ResumeID  string
	HubEpoch  string
}

type daemonBrokerLaunch struct {
	launchID string
	broker   *interactiveartifacts.LaunchBroker
	stream   appwire.Transport
	writer   *os.File

	childRead  *os.File
	childWrite *os.File
	done       chan error
	startOnce  sync.Once
}

func attachDaemonBrokerLaunch(cmd *exec.Cmd, cfg daemonBrokerLaunchConfig) (*daemonBrokerLaunch, error) {
	if cmd == nil || cfg.Authority == nil || cfg.HubEpoch == "" {
		return nil, errors.New("artifact broker launch configuration incomplete")
	}
	childRead, parentWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create artifact broker input pipe: %w", err)
	}
	parentRead, childWrite, err := os.Pipe()
	if err != nil {
		_ = childRead.Close()
		_ = parentWrite.Close()
		return nil, fmt.Errorf("create artifact broker output pipe: %w", err)
	}
	stream := interactiveartifacts.NewPrivateBrokerTransport(parentRead, parentWrite)
	launchID := interactiveartifacts.NewBrokerID()
	readFD := 3 + len(cmd.ExtraFiles)
	writeFD := readFD + 1
	cmd.ExtraFiles = append(cmd.ExtraFiles, childRead, childWrite)
	cmd.Args = append(cmd.Args,
		"--artifact-broker-read-fd="+strconv.Itoa(readFD),
		"--artifact-broker-write-fd="+strconv.Itoa(writeFD),
	)
	return &daemonBrokerLaunch{
		launchID: launchID,
		broker: interactiveartifacts.NewLaunchBroker(interactiveartifacts.LaunchBrokerConfig{
			Transport: stream,
			Authority: cfg.Authority,
			LaunchID:  launchID,
			HubEpoch:  cfg.HubEpoch,
			ProjectID: cfg.ProjectID,
			ResumeID:  cfg.ResumeID,
		}),
		stream: stream, writer: parentWrite, childRead: childRead, childWrite: childWrite, done: make(chan error, 1),
	}, nil
}

// afterStart transfers descriptor ownership to the child on success and starts
// the Hub side of the private handshake. Parent copies of both child ends close
// on every path, including Start failure.
func (l *daemonBrokerLaunch) afterStart(startErr error) {
	l.startOnce.Do(func() {
		_ = l.childRead.Close()
		_ = l.childWrite.Close()
		if startErr != nil {
			_ = l.stream.Close()
			l.done <- startErr
			close(l.done)
			return
		}
		if err := interactiveartifacts.WriteLaunchCorrelation(l.writer, l.launchID); err != nil {
			_ = l.stream.Close()
			l.done <- err
			close(l.done)
			return
		}
		go func() {
			l.done <- l.broker.Serve(context.Background())
			close(l.done)
		}()
	})
}

func (l *daemonBrokerLaunch) finalize(entry rendezvous.Entry) error {
	return l.broker.FinalizeLaunch(context.Background(), interactiveartifacts.DaemonIdentity(entry))
}

func (l *daemonBrokerLaunch) seal(err error) { l.broker.Seal(err) }

func (l *daemonBrokerLaunch) close() error {
	l.broker.Seal(nil)
	return l.stream.Close()
}
