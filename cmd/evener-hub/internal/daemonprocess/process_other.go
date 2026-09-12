//go:build !darwin && !linux

package daemonprocess

import "errors"

// NewController refuses platforms without generation-bound process signaling.
func NewController() Controller {
	return controller{bind: func(int) (processHandle, error) {
		return nil, errors.New("verified daemon termination is unsupported on this platform")
	}}
}
