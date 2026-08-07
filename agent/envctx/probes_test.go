package envctx

import "testing"

func TestLoadWarningThreshold(t *testing.T) {
	if got := loadWarning(3.9, 4); got != "" {
		t.Fatalf("below threshold must be nominal, got %q", got)
	}
	if got := loadWarning(8.5, 4); got != "load pressure: 8.5 (4 cores)" {
		t.Fatalf("above threshold: %q", got)
	}
}

func TestParseLoadAvgOutput(t *testing.T) {
	// darwin `sysctl -n vm.loadavg` prints "{ 2.16 3.57 4.34 }";
	// linux /proc/loadavg starts "2.16 3.57 4.34 ...".
	for _, in := range []string{"{ 2.16 3.57 4.34 }", "2.16 3.57 4.34 1/234 5678"} {
		got, ok := parseLoad1(in)
		if !ok || got != 2.16 {
			t.Fatalf("parseLoad1(%q) = %v, %v", in, got, ok)
		}
	}
	if _, ok := parseLoad1("nonsense"); ok {
		t.Fatal("garbage must not parse")
	}
}

func TestDiskWarningThreshold(t *testing.T) {
	if got := diskWarning(0.89); got != "" {
		t.Fatalf("below threshold must be nominal, got %q", got)
	}
	if got := diskWarning(0.93); got != "disk pressure: volume 93% full" {
		t.Fatalf("above threshold: %q", got)
	}
}

func TestDefaultProbesNeverPanic(t *testing.T) {
	p := DefaultProbes()
	_ = p.Load()
	_ = p.Memory()
	_ = p.Disk("/")
}
