package buildinfo

import "fmt"

var (
	GitSHA    string // set via -ldflags "-X primeradiant.com/evener/buildinfo.GitSHA=..."
	GitDirty  string // "true" or ""
	BuildTime string // ISO8601
	Channel   string // release, snapshot, or empty for local dev builds
	// ReleaseTag is the GitHub release tag this build was published from, set
	// via -ldflags for a release build (.goreleaser.yml). It is what the
	// installer fallback pins EVENER_INSTALL_VERSION to: install.sh treats that
	// variable as a release tag, so Version() (a short SHA, possibly "-dirty")
	// is not a usable substitute.
	ReleaseTag string
)

func Version() string {
	if GitSHA == "" {
		return "dev"
	}
	v := GitSHA
	if GitDirty == "true" {
		v += "-dirty"
	}
	return v
}

func VersionLong() string {
	if GitSHA == "" {
		return "dev (no build info)"
	}
	v := GitSHA
	if GitDirty == "true" {
		v += "-dirty"
	}
	if BuildTime != "" {
		v += fmt.Sprintf(" (%s)", BuildTime)
	}
	return v
}

func BuildChannel() string {
	if Channel == "" {
		return "dev"
	}
	return Channel
}

func UpgradeChannel() string {
	if Channel == "snapshot" {
		return "snapshot"
	}
	return "release"
}
