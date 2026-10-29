package componentlog

import (
	"bytes"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestRecordCarriesComponentMessageAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	l := NewWriter("hub", &buf).With("session", "s1")
	l.Info("probe failed", "attempt", 3)

	line := strings.TrimSuffix(buf.String(), "\n")
	fields := strings.Fields(line)
	if fields[0] != "[hub]" {
		t.Fatalf("component tag = %q", fields[0])
	}
	got := map[string]bool{}
	for _, f := range fields[1:] {
		got[f] = true
	}
	for _, want := range []string{"probe", "failed", "session=s1", "attempt=3"} {
		if !got[want] {
			t.Errorf("missing field %q in %q", want, line)
		}
	}
	if strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("record is not exactly one line: %q", buf.String())
	}
}

func TestConcurrentRecordsStayOneLineEach(t *testing.T) {
	var buf bytes.Buffer
	l := NewWriter("hub", &buf)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { l.Info("tick", "k", "v") })
	}
	wg.Wait()
	for line := range strings.SplitSeq(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		if !strings.HasPrefix(line, "[hub] ") || !strings.HasSuffix(line, "k=v") {
			t.Fatalf("interleaved record: %q", line)
		}
	}
}

func TestControlCharactersCannotForgeRecords(t *testing.T) {
	var buf bytes.Buffer
	l := NewWriter("hub", &buf)
	l.Info("bad\n[hub] forged", "path", "a\r\n[hub] forged2", "err", errors.New("x\ny"))
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("record spans %d lines: %q", n, buf.String())
	}
}

func TestGroupsKeepAttrsDistinct(t *testing.T) {
	var buf bytes.Buffer
	l := NewWriter("hub", &buf).With("id", "parent").WithGroup("child").With("id", "kid")
	l.Info("m", "id", "leaf", slog.Group("g", "id", "inner"))
	got := map[string]bool{}
	for _, f := range strings.Fields(buf.String())[2:] {
		got[f] = true
	}
	for _, want := range []string{"id=parent", "child.id=kid", "child.id=leaf", "child.g.id=inner"} {
		if !got[want] {
			t.Errorf("missing %q in %q", want, buf.String())
		}
	}
}

func TestControlCharactersInKeysAndGroupsCannotForgeRecords(t *testing.T) {
	var buf bytes.Buffer
	l := NewWriter("hub", &buf).WithGroup("g\n[hub] forged")
	l.Info("m", "x\n[hub] forged", "v")
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("record spans %d lines: %q", n, buf.String())
	}
}

func TestUnnamedAndEmptyBoundAttrsInsideGroup(t *testing.T) {
	var buf bytes.Buffer
	l := NewWriter("hub", &buf).WithGroup("child").With(slog.Group("", "id", "kid"), slog.Attr{})
	l.Info("m")
	got := strings.Fields(buf.String())[2:]
	if len(got) != 1 || got[0] != "child.id=kid" {
		t.Fatalf("attrs = %v", got)
	}
}

func TestDelimitersInValuesCannotForgeFields(t *testing.T) {
	const value = "/tmp/a reason=other"
	var buf bytes.Buffer
	NewWriter("hub", &buf).Info("m", "path", value)
	_, quoted, ok := strings.Cut(strings.TrimSuffix(buf.String(), "\n"), " path=")
	if !ok {
		t.Fatalf("no path field in %q", buf.String())
	}
	if got, err := strconv.Unquote(quoted); err != nil || got != value {
		t.Fatalf("value = %q, %v; want %q as one quoted field", got, err, value)
	}
}
