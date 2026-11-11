package main

import (
	"errors"
	"fmt"
)

// Reasons the generator rejects a doc. Each rejection's message names the file
// involved; its reason lets a caller tell the rejections apart with errors.Is.
// The reasons for rejecting a FAMILY FILE live with the parser, in
// maketargetsdoc/makefile, which is where ParseFamily returns them.
var (
	errEndBeforeBegin       = errors.New("END marker before its BEGIN marker")
	errMalformedBeginMarker = errors.New("malformed BEGIN marker")
	errRegionCount          = errors.New("doc does not have exactly one GENERATED region")
	errEndMarkerCount       = errors.New("doc does not have exactly one END marker")
	errRegionForOtherFamily = errors.New("GENERATED region is for another family")
	errNoMarkedRegion       = errors.New("doc has no marked region")

	errNoFamilyFiles          = errors.New("no family files")
	errNoDestinationDoc       = errors.New("family has no destination doc")
	errRegionForMissingFamily = errors.New("marked region names a family that does not exist")
	errRegionInWrongDoc       = errors.New("marked region is in the wrong doc")
)

// rejectionError is a rejection with its reason: Error is the full message a
// person reads, and Unwrap is the reason sentinel.
type rejectionError struct {
	reason error
	msg    string
}

func (e *rejectionError) Error() string { return e.msg }

func (e *rejectionError) Unwrap() error { return e.reason }

// reject formats a rejection message carrying reason.
func reject(reason error, format string, args ...any) error {
	return &rejectionError{reason: reason, msg: fmt.Sprintf(format, args...)}
}
