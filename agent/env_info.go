package agent

import (
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/schema"
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
	return schema.EnvironmentInfo{
		WorkingDir: wd,
		Platform:   plat,
		OSVersion:  osv,
		Today:      clk.Now().UTC().Format("2006-01-02"),
		Workspace:  ScanWorkspace(wd),
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
