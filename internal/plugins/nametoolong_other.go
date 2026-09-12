//go:build !unix

package plugins

// isNameTooLong reports whether err is the filesystem refusing a path for being
// longer than it takes. The unix version asks the errno; elsewhere the store
// bounds its names by other means, so this answers no rather than guessing at
// another platform's error.
func isNameTooLong(error) bool { return false }
