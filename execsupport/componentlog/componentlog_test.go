package componentlog

import (
	"bytes"
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Info("tick", "k", "v")
		}()
	}
	wg.Wait()
	for _, line := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		if !strings.HasPrefix(line, "[hub] ") || !strings.HasSuffix(line, "k=v") {
			t.Fatalf("interleaved record: %q", line)
		}
	}
}
