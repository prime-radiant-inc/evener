package appsource

import "errors"

// ErrSourceNotFound marks a ref naming a source this hub does not serve. The
// refusal names the source, so an operator can see which one is missing, and
// callers match it with errors.Is rather than the message: a miss can mean the
// source's registration has not landed yet, and must never be retargeted to
// another source.
var ErrSourceNotFound = errors.New("source not found")
