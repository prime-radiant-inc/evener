//go:build !darwin && !linux

package envctx

import "time"

// DefaultProbes on unsupported platforms reads everything as nominal.
func DefaultProbes() Probes { return Probes{Now: time.Now} }
