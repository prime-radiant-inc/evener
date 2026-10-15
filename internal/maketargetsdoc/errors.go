package main

import (
	"errors"
	"fmt"
)

// Reasons the generator rejects a family file or a doc. Each rejection's
// message names the line, target or file involved; its reason lets a caller
// tell the rejections apart with errors.Is.
var (
	errBlockDetached            = errors.New("## block separated from its rule")
	errBlockOnDirective         = errors.New("## block above a directive")
	errBlockOnTargetVariable    = errors.New("## block above a target-specific variable line")
	errTargetUnannotated        = errors.New("target has no ## block")
	errBlockWithoutSummary      = errors.New("## block has no summary line")
	errBlockUnattached          = errors.New("## block never attached to a rule")
	errMalformedAnnotationLine  = errors.New("malformed ## line")
	errContinuationWithoutBlock = errors.New("continuation line outside a ## block")

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
