// Package oldlog is the shop's in-house leveled logger.
package oldlog

import "log"

// Infof logs at info level.
func Infof(format string, args ...any) { log.Printf("INFO "+format, args...) }

// Warnf logs at warn level.
func Warnf(format string, args ...any) { log.Printf("WARN "+format, args...) }

// Errorf logs at error level.
func Errorf(format string, args ...any) { log.Printf("ERROR "+format, args...) }
