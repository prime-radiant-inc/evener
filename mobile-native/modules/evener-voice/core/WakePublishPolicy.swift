/// Decides whether the periodic armed tick republishes the full voice state.
/// A live classifier score changes on almost every 80 ms hop, so the score is
/// not a publish trigger outside trials; the debug panel refreshes scores at
/// its 500 ms cadence only while a trial runs.
public enum WakePublishPolicy {
    public static func shouldPublish(
        trialRunning: Bool,
        wakeState: String,
        publishedWakeState: String,
        wakeError: String,
        publishedWakeError: String
    ) -> Bool {
        trialRunning || wakeState != publishedWakeState || wakeError != publishedWakeError
    }
}
