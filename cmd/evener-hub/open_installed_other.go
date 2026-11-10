//go:build !unix

package hub

import "os"

// openInstalledRead matches the unix signature; non-unix platforms have no
// FIFO open to block on, and the descriptor check still rejects other types.
func openInstalledRead(path string) (*os.File, error) {
	return os.Open(path)
}
