// Shared by the iOS probe and the command-line control check.
struct PlaybackFence: Sendable {
    private(set) var generation: UInt64 = 0

    @discardableResult
    mutating func invalidate() -> UInt64 {
        generation += 1
        return generation
    }

    func accepts(_ candidate: UInt64) -> Bool {
        candidate == generation
    }
}
