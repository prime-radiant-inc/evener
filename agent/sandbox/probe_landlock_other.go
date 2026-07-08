//go:build !linux

package sandbox

// probeLandlockABI reports Landlock as unavailable on every non-Linux OS
// (Landlock is a Linux LSM). darwin uses Seatbelt; other OSes refuse sandboxing.
func probeLandlockABI() int { return 0 }
