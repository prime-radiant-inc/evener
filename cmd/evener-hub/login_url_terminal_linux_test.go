//go:build linux

package hub

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestStartupLoginOutputTerminalFile(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	master, slave := openLoginPTY(t)
	defer master.Close()
	defer slave.Close()
	if !isTerminalWriter(slave) {
		t.Fatal("terminal descriptor not identified")
	}
	report, stop := startPublicationHub(t, path, state, slave)
	defer stop()
	expected := []byte(report.Running.LocalURL + "/auth/" + publicationToken)
	output := readLoginPTY(t, master)
	if !bytes.Contains(output, expected) {
		t.Fatal("interactive startup did not disclose its working login link")
	}
	if _, err := os.Stat(filepath.Join(state, "hub-endpoint.json")); err != nil {
		t.Fatal(err)
	}
}

func openLoginPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pseudo terminal unavailable: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
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
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

func readLoginPTY(t *testing.T, master *os.File) []byte {
	t.Helper()
	var output []byte
	deadline := time.Now().Add(5 * time.Second)
	for !bytes.Contains(output, []byte(publicationToken)) && time.Now().Before(deadline) {
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
	return output
}

func TestStartupLoginOriginResolvedOnce(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "0.0.0.0:0")
	prior := hubHostname
	calls := 0
	hubHostname = func() (string, error) {
		calls++
		if calls == 1 {
			return "first.example.test", nil
		}
		return "second.example.test", nil
	}
	defer func() { hubHostname = prior }()
	master, slave := openLoginPTY(t)
	report, stop := startPublicationHub(t, path, state, slave)
	defer stop()
	output := readLoginPTY(t, master)
	_, port, err := net.SplitHostPort(report.Running.BoundAddr)
	if err != nil {
		t.Fatal(err)
	}
	wantHost := net.JoinHostPort("first.example.test", port)
	loginFound := false
	for field := range strings.FieldsSeq(string(output)) {
		parsed, err := url.Parse(field)
		if err == nil && parsed.Path == "/auth/"+publicationToken {
			loginFound = true
			if parsed.Scheme != "http" || parsed.Host != wantHost {
				t.Errorf("login destination=%s, want host %s", parsed.Redacted(), wantHost)
			}
		}
	}
	if !loginFound || calls != 1 {
		t.Fatalf("interactive login found=%v hostname lookups=%d", loginFound, calls)
	}
}
