package hub

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestHubKnowsRefDoesNotOpenTranscript pins that answering saved-session
// existence never opens the saved transcript. The scan test proves one kind of
// transcript work is absent; this watches the transcript's own inode for
// IN_OPEN, so it covers any other read the existence check might perform.
//
// The watch is installed after the fixture and its index are built, and the
// drain is nonblocking: EAGAIN, not a sleep or deadline, ends it.
func TestHubKnowsRefDoesNotOpenTranscript(t *testing.T) {
	cfg, entry, path := seedIndexedPastSession(t, completedExecution("turn_known"))

	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		t.Fatalf("inotify init: %v", err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if _, err := unix.InotifyAddWatch(fd, path, unix.IN_OPEN); err != nil {
		t.Fatalf("inotify add watch on %s: %v", filepath.Base(path), err)
	}

	if !hubKnowsRef(cfg, localAppRef(entry.Meta.ID)) {
		t.Fatal("known session rejected")
	}

	buf := make([]byte, 4096)
	for {
		n, err := unix.Read(fd, buf)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) {
				return
			}
			t.Fatalf("inotify read: %v", err)
		}
		if n == 0 {
			return
		}
		for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
			mask := binary.NativeEndian.Uint32(buf[offset+4:])
			nameLen := int(binary.NativeEndian.Uint32(buf[offset+12:]))
			if mask&unix.IN_OPEN != 0 {
				t.Fatalf("hubKnowsRef opened the saved transcript %s", filepath.Base(path))
			}
			offset += unix.SizeofInotifyEvent + nameLen
		}
	}
}
