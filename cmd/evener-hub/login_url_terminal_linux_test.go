//go:build linux

package hub

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestStartupLoginOutputTerminalFile(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pseudo terminal unavailable: %v", err)
	}
	defer master.Close()
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skipf("pseudo terminal unlock unavailable: %v", err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skipf("pseudo terminal slave unavailable: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pseudo terminal slave unavailable: %v", err)
	}
	defer slave.Close()
	if !isTerminalWriter(slave) {
		t.Fatal("terminal descriptor not identified")
	}
	report, stop := startPublicationHub(t, path, state, slave)
	defer stop()
	expected := []byte(report.Running.LocalURL + "/auth/" + publicationToken)
	var output []byte
	deadline := time.Now().Add(5 * time.Second)
	for !bytes.Contains(output, expected) && time.Now().Before(deadline) {
		ready, err := unix.Poll([]unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}, 250)
		if err != nil {
			t.Fatal(err)
		}
		if ready == 0 {
			continue
		}
		block := make([]byte, 4096)
		n, err := master.Read(block)
		if err != nil {
			t.Fatal(err)
		}
		output = append(output, block[:n]...)
	}
	if !bytes.Contains(output, expected) {
		t.Fatal("interactive startup did not disclose its working login link")
	}
	if _, err := os.Stat(filepath.Join(state, "hub-endpoint.json")); err != nil {
		t.Fatal(err)
	}
}
