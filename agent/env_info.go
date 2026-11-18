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
	plat := ""
	osv := ""
	if env != nil {
		wd = env.WorkingDirectory()
		plat = env.Platform()
		osv = env.OSVersion()
	}
	// The record is the only thing later identities can be derived from, so it is
	// stored absolute: a project ID is a hash of an absolute canonical path, and
	// --dir . leaves the directory the process launched in as a relative string.
	// Hashing that string as given yields an ID no checkout has, which makes a
	// lane created from this checkout read as a different repository's.
	if wd != "" {
		if absolute, err := filepath.Abs(wd); err == nil {
			wd = absolute
		}
	}
	return schema.EnvironmentInfo{
		WorkingDir: wd,
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
