package buildinfo

import "testing"

func FuzzVersionFormatting(f *testing.F) {
	f.Add("", "", "", "")
	f.Add("abc123", "true", "2026-07-11T00:00:00Z", "snapshot")

	f.Fuzz(func(t *testing.T, sha, dirty, buildTime, channel string) {
		oldSHA, oldDirty, oldBuildTime, oldChannel := GitSHA, GitDirty, BuildTime, Channel
		GitSHA, GitDirty, BuildTime, Channel = sha, dirty, buildTime, channel
		t.Cleanup(func() {
			GitSHA, GitDirty, BuildTime, Channel = oldSHA, oldDirty, oldBuildTime, oldChannel
		})

		wantVersion := sha
		if sha == "" {
			wantVersion = "dev"
		} else if dirty == "true" {
			wantVersion += "-dirty"
		}
		if got := Version(); got != wantVersion {
			t.Fatalf("Version() = %q, want %q", got, wantVersion)
		}

		wantLong := "dev (no build info)"
		if sha != "" {
			wantLong = wantVersion
			if buildTime != "" {
				wantLong += " (" + buildTime + ")"
			}
		}
		if got := VersionLong(); got != wantLong {
			t.Fatalf("VersionLong() = %q, want %q", got, wantLong)
		}

		wantBuildChannel := channel
		if wantBuildChannel == "" {
			wantBuildChannel = "dev"
		}
		if got := BuildChannel(); got != wantBuildChannel {
			t.Fatalf("BuildChannel() = %q, want %q", got, wantBuildChannel)
		}
		wantUpgradeChannel := "release"
		if channel == "snapshot" {
			wantUpgradeChannel = "snapshot"
		}
		if got := UpgradeChannel(); got != wantUpgradeChannel {
			t.Fatalf("UpgradeChannel() = %q, want %q", got, wantUpgradeChannel)
		}
	})
}
