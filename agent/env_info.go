package agent

import (
	"path/filepath"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/hostresources"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/schema"
)

// envInfoFromEnv builds a schema.EnvironmentInfo from the execution environment,
// stamping today's date (read from clk) and the detected workspace layout.
func envInfoFromEnv(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
	wd := ""
	launchSpelling := ""
	plat := ""
	osv := ""
	if env != nil {
		wd = env.WorkingDirectory()
		plat = env.Platform()
		osv = env.OSVersion()
		// The environment resolves its root at construction, so WorkingDirectory is
		// the physical directory and the spelling the session was launched with is
		// only available from the environment itself.
		if speller, ok := env.(interface{ LaunchSpelling() string }); ok {
			launchSpelling = speller.LaunchSpelling()
		}
	}
	// The record is the only thing later identities can be derived from, so it is
	// stored absolute: a project ID is a hash of an absolute canonical path, and
	// --dir . leaves the directory the process launched in as a relative string.
	// Hashing that string as given yields an ID no checkout has, which makes a
	// lane created from this checkout read as a different repository's.
	//
	// It goes through canonicalStateDir rather than filepath.Abs for the symlink
	// half of that: Abs cleans ".." lexically, while the kernel resolves it
	// against the physical parent, so `--dir link/..` would record a directory the
	// commands never ran in and the startup snapshot would then describe a
	// checkout the session is not in.
	wd = canonicalStateDir(wd)
	// The launch spelling is kept only when it is an already-clean absolute alias
	// of the resolved root. It is what a file reference written by the user names,
	// and the transcript client binds absolute references by lexical prefix, so
	// erasing it makes those references stop opening even though the file is still
	// there. A spelling that is not clean (`.`, `link/..`) never prefixes a real
	// reference, so it is not a binding alias and is dropped. See
	// EnvironmentInfo.LaunchDir: identity reads WorkingDir, binding reads this.
	launchDir := ""
	if launchSpelling != "" && filepath.IsAbs(launchSpelling) && filepath.Clean(launchSpelling) == launchSpelling && launchSpelling != wd {
		launchDir = launchSpelling
	}
	return schema.EnvironmentInfo{
		WorkingDir: wd,
		LaunchDir:  launchDir,
		Platform:   plat,
		OSVersion:  osv,
		Today:      clk.Now().UTC().Format("2006-01-02"),
		Workspace:  ScanWorkspace(wd),
		Resources:  hostresources.CapsFromEnv(env),
	}
}

// snapshotEnvironmentInfo reads the environment snapshot used by this session.
// Production delegates to envInfoFromEnv; package-agent tests may inject only
// this host boundary while still exercising the real Session lifecycle.
func (s *Session) snapshotEnvironmentInfo(env execenv.ExecutionEnvironment) schema.EnvironmentInfo {
	if snapshot := s.cfg.testOnly.environmentInfo; snapshot != nil {
		return snapshot(env, s.sclock())
	}
	return envInfoFromEnv(env, s.sclock())
}
