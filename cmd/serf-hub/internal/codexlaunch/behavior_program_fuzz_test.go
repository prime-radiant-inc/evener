package codexlaunch

import "testing"

// FuzzCodexLaunchBehaviorProgram replays one deterministic behavioral contract selected by the
// fuzz input. The seed corpus covers every production branch; mutation varies
// ordering and repetition without relying on network, wall clock, or host state.
func FuzzCodexLaunchBehaviorProgram(f *testing.F) {
	checks := []func(*testing.T){
		checkSeed100LauncherConfigurationAndCache,
		checkSeed100LaunchArgumentsEnvironmentAndScanning,
		checkSeed100ReadyAndEndpointConversion,
		checkSeed100ReadyRequestConstructionFailure,
		checkSeed100LaunchFailureModes,
		checkSeed100EndpointDiscoveryAndErroredExit,
		checkSeed100LaunchPipeFailures,
		checkSeed100EnsureSourceLaunchAndShutdown,
		checkSeed100Shutdown,
		checkSeed100ProductionRuntimeAdaptersWithoutStartingChild,
	}
	for i := range checks {
		f.Add(uint8(i))
	}
	f.Fuzz(func(t *testing.T, selector uint8) {
		checks[int(selector)%len(checks)](t)
	})
}
