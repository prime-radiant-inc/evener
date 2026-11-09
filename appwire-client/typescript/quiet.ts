// How long a running row or transcript liveness line may stay silent before
// every surface counts it quiet. One constant because every surface must flip
// at the same instant; #4520 found the shared client and the web transcript
// each carrying their own 20-second literal. Distinct from sessionActivity's
// three-minute QUIET_AFTER_MS, which is the session-state rule.
export const RUNNING_QUIET_AFTER_MS = 20_000;
