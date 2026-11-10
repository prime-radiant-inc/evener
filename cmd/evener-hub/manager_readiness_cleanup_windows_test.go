//go:build windows

package hub

// The signal-based cleanup fixture is excluded from Windows builds. TestMain
// still names its entry point, so an accidental invocation must fail explicitly.
func runManagerCleanupServeHelper() {
	panic("Manager cleanup process helper requires Unix signals")
}
