package agent

import (
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/schema"
)

// envInfoFromEnv builds a schema.EnvironmentInfo from the execution environment,
// stamping today's date (read from clk) and the detected workspace layout.
func envInfoFromEnv(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
	return envInfoFromEnvWithResourceCaps(env, clk, true)
}

func envInfoFromEnvWithResourceCaps(env execenv.ExecutionEnvironment, clk clock.Clock, collectCaps bool) schema.EnvironmentInfo {
	wd := ""
	plat := ""
	osv := ""
	if env != nil {
		wd = env.WorkingDirectory()
		plat = env.Platform()
		osv = env.OSVersion()
	}
	var caps resourceCaps
	if collectCaps {
		caps = probeEffectiveResourceCaps(env, wd)
	}
	return schema.EnvironmentInfo{
		WorkingDir: wd,
		Platform:   plat,
		OSVersion:  osv,
		Today:      clk.Now().UTC().Format("2006-01-02"),
		CPUs:       caps.CPUs,
		MemoryMB:   caps.MemoryMB,
		Workspace:  ScanWorkspace(wd),
		Resources:  resourceCapsFromEnv(env),
	}
}

// snapshotEnvironmentInfo reads the environment snapshot used by this session.
// Production delegates to envInfoFromEnv; package-agent tests may inject only
// this host boundary while still exercising the real Session lifecycle.
func (s *Session) snapshotEnvironmentInfo(env execenv.ExecutionEnvironment) schema.EnvironmentInfo {
	if snapshot := s.cfg.testOnly.environmentInfo; snapshot != nil {
		return snapshot(env, s.sclock())
	}
	return envInfoFromEnvWithResourceCaps(env, s.sclock(), !s.cfg.testOnly.skipGitSnapshot)
}
