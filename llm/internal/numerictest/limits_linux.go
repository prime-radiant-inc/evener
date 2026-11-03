//go:build linux

package numerictest

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func limitChild(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		t.Fatal(err)
	}
	var pages uint64
	if _, err := fmt.Sscan(string(data), &pages); err != nil {
		t.Fatal(err)
	}
	as := pages*uint64(os.Getpagesize()) + (512 << 20)
	for _, v := range []struct {
		resource int
		limit    uint64
	}{{unix.RLIMIT_CPU, 10}, {unix.RLIMIT_AS, as}} {
		lim := unix.Rlimit{Cur: v.limit, Max: v.limit}
		if err := unix.Setrlimit(v.resource, &lim); err != nil {
			t.Fatal(err)
		}
		var got unix.Rlimit
		if err := unix.Getrlimit(v.resource, &got); err != nil {
			t.Fatal(err)
		}
		if got != lim {
			t.Fatalf("limit readback: got %+v want %+v", got, lim)
		}
	}
	t.Logf("hard containment installed and read back: RLIMIT_CPU=10 RLIMIT_AS=%d current_virtual=%d", as, pages*uint64(os.Getpagesize()))
}
