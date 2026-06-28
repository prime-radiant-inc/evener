package buildinfo

import "testing"

func TestVersionDev(t *testing.T) {
	// Zero-value vars → dev build.
	saved := GitSHA
	savedChannel := Channel
	GitSHA = ""
	Channel = ""
	defer func() { GitSHA = saved; Channel = savedChannel }()

	if got := Version(); got != "dev" {
		t.Errorf("Version() = %q, want %q", got, "dev")
	}
	if got := VersionLong(); got != "dev (no build info)" {
		t.Errorf("VersionLong() = %q, want %q", got, "dev (no build info)")
	}
	if got := BuildChannel(); got != "dev" {
		t.Errorf("BuildChannel() = %q, want %q", got, "dev")
	}
	if got := UpgradeChannel(); got != "release" {
		t.Errorf("UpgradeChannel() = %q, want %q", got, "release")
	}
}

func TestVersionClean(t *testing.T) {
	savedSHA := GitSHA
	savedDirty := GitDirty
	savedTime := BuildTime
	defer func() { GitSHA = savedSHA; GitDirty = savedDirty; BuildTime = savedTime }()

	GitSHA = "a69df56"
	GitDirty = ""
	BuildTime = "2026-02-28T20:00:00Z"

	if got := Version(); got != "a69df56" {
		t.Errorf("Version() = %q, want %q", got, "a69df56")
	}
	want := "a69df56 (2026-02-28T20:00:00Z)"
	if got := VersionLong(); got != want {
		t.Errorf("VersionLong() = %q, want %q", got, want)
	}
}

func TestVersionDirty(t *testing.T) {
	savedSHA := GitSHA
	savedDirty := GitDirty
	savedTime := BuildTime
	defer func() { GitSHA = savedSHA; GitDirty = savedDirty; BuildTime = savedTime }()

	GitSHA = "a69df56"
	GitDirty = "true"
	BuildTime = ""

	if got := Version(); got != "a69df56-dirty" {
		t.Errorf("Version() = %q, want %q", got, "a69df56-dirty")
	}
	if got := VersionLong(); got != "a69df56-dirty" {
		t.Errorf("VersionLong() = %q, want %q (no build time)", got, "a69df56-dirty")
	}
}

func TestVersionDirtyWithBuildTime(t *testing.T) {
	savedSHA := GitSHA
	savedDirty := GitDirty
	savedTime := BuildTime
	defer func() { GitSHA = savedSHA; GitDirty = savedDirty; BuildTime = savedTime }()

	GitSHA = "a69df56"
	GitDirty = "true"
	BuildTime = "2026-02-28T20:00:00Z"

	if got := Version(); got != "a69df56-dirty" {
		t.Errorf("Version() = %q, want %q", got, "a69df56-dirty")
	}
	want := "a69df56-dirty (2026-02-28T20:00:00Z)"
	if got := VersionLong(); got != want {
		t.Errorf("VersionLong() = %q, want %q", got, want)
	}
}

func TestUpgradeChannelTracksSnapshotOnly(t *testing.T) {
	saved := Channel
	defer func() { Channel = saved }()

	Channel = "snapshot"
	if got := BuildChannel(); got != "snapshot" {
		t.Errorf("BuildChannel() = %q, want snapshot", got)
	}
	if got := UpgradeChannel(); got != "snapshot" {
		t.Errorf("UpgradeChannel() = %q, want snapshot", got)
	}

	Channel = "release"
	if got := UpgradeChannel(); got != "release" {
		t.Errorf("release UpgradeChannel() = %q, want release", got)
	}
}
