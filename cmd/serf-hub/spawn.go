package main

// findTemplate returns the named SpawnTemplate from the config.
func findTemplate(cfg Config, name string) (SpawnTemplate, bool) {
	for _, t := range cfg.SpawnTemplates {
		if t.Name == name {
			return t, true
		}
	}
	return SpawnTemplate{}, false
}

// buildSpawnArgs assembles the arg slice for `serf serve` from a template
// and a working directory.
//
// Always passes --addr 127.0.0.1:0 so the daemon binds an ephemeral port,
// which it reports via its rendezvous file. Empty fields in the template
// are omitted so `serf serve` can fall back to its environment.
func buildSpawnArgs(t SpawnTemplate, workingDir string) []string {
	args := []string{"--addr", "127.0.0.1:0"}
	if workingDir != "" {
		args = append(args, "--dir", workingDir)
	}
	if t.Provider != "" {
		args = append(args, "--provider", t.Provider)
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	if t.Agent != "" {
		args = append(args, "--agent", t.Agent)
	}
	if t.ReasoningEffort != "" {
		args = append(args, "--reasoning-effort", t.ReasoningEffort)
	}
	return args
}
