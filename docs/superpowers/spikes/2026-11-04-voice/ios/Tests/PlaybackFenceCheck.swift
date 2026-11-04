@main
struct PlaybackFenceCheck {
    static func main() {
        var fence = PlaybackFence()
        let old = fence.generation
        precondition(fence.accepts(old))
        let stopped = fence.invalidate()
        precondition(!fence.accepts(old), "stopped output must not return")
        precondition(fence.accepts(stopped))
        let replaced = fence.invalidate()
        precondition(!fence.accepts(stopped), "replaced output must not return")
        precondition(fence.accepts(replaced))
        print("PASS: current accepted, stopped rejected, replacement accepted, previous rejected")
    }
}
