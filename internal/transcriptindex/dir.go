package transcriptindex

// DirFor is the sidecar directory the daemon and the hub share for a
// transcript: the location every Open of that transcript's index must agree
// on, so a rebuild by one is visible to the other.
func DirFor(transcriptPath string) string { return transcriptPath + ".index" }
