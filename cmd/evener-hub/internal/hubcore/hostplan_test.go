package hubcore

// The facts revision (deploy pipeline 08b §1) is the digest a plan binds and a
// deploy compares, so its two properties are pinned here: the same fact set
// always digests the same — with "no roots" and "no flags" having one spelling —
// and any change to a covered field moves it.

import (
	"testing"
	"time"
)

func hostPlanFactsFixture() HostPlanFacts {
	return HostPlanFacts{
		OS:          "linux",
		Arch:        "amd64",
		Home:        "/home/ops",
		Roots:       []string{"/srv/work", "/srv/other"},
		UID:         "1000",
		Version:     "v1.0.0",
		Protocol:    "3",
		LaunchFlags: []string{"--config", "/etc/evener.toml"},
		CapturedAt:  time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
	}
}

func TestHostPlanFactsRevisionPinsTheCoveredFields(t *testing.T) {
	base := hostPlanFactsFixture()
	digest := base.Revision()
	if len(digest) != 64 {
		t.Fatalf("digest = %q, want a 64-character hex digest", digest)
	}
	if again := hostPlanFactsFixture().Revision(); again != digest {
		t.Fatalf("the same facts digested differently: %q then %q", digest, again)
	}
	// The capture time is not part of the digest: freshness is bound separately.
	later := base
	later.CapturedAt = base.CapturedAt.Add(time.Hour)
	if later.Revision() != digest {
		t.Fatal("the capture time moved the facts digest")
	}
	// "No roots" has one spelling.
	noRoots := base
	noRoots.Roots = nil
	emptyRoots := base
	emptyRoots.Roots = []string{}
	if noRoots.Revision() != emptyRoots.Revision() {
		t.Fatal("a nil root list and an empty one digested differently")
	}
	// Every covered field moves it, and so does the shape of the lists.
	cases := map[string]func(*HostPlanFacts){
		"os":           func(f *HostPlanFacts) { f.OS = "darwin" },
		"arch":         func(f *HostPlanFacts) { f.Arch = "arm64" },
		"home":         func(f *HostPlanFacts) { f.Home = "/root" },
		"uid":          func(f *HostPlanFacts) { f.UID = "0" },
		"version":      func(f *HostPlanFacts) { f.Version = "v1.0.1" },
		"protocol":     func(f *HostPlanFacts) { f.Protocol = "4" },
		"root order":   func(f *HostPlanFacts) { f.Roots = []string{"/srv/other", "/srv/work"} },
		"root content": func(f *HostPlanFacts) { f.Roots = []string{"/srv/work"} },
		"flag order":   func(f *HostPlanFacts) { f.LaunchFlags = []string{"/etc/evener.toml", "--config"} },
		"flag content": func(f *HostPlanFacts) { f.LaunchFlags = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := hostPlanFactsFixture()
			mutate(&changed)
			if changed.Revision() == digest {
				t.Fatalf("changing %s left the digest at %q", name, digest)
			}
		})
	}
}
