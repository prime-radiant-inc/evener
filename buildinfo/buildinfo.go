package buildinfo

import "fmt"

var (
	GitSHA    string // set via -ldflags "-X primeradiant.com/serf/buildinfo.GitSHA=..."
	GitDirty  string // "true" or ""
	BuildTime string // ISO8601
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
