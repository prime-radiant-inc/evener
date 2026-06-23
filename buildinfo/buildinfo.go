package buildinfo

import "fmt"

var (
	GitSHA    string // set via -ldflags "-X primeradiant.com/serf/buildinfo.GitSHA=..."
	GitDirty  string // "true" or ""
	BuildTime string // ISO8601
	Channel   string // release, snapshot, or empty for local dev builds
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
