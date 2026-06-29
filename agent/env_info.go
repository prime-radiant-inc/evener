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
