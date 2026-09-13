//go:build !unix

package plugins

// pathCannotExist reports whether err says the path names nothing that can be
// there at all. The unix version asks the errnos; the store bounds its names by
// other means elsewhere, so this answers no rather than guessing at another
// platform's errors.
func pathCannotExist(error) bool { return false }
