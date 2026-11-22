// Package pdfslice counts PDF pages and extracts a page range entirely in
// memory with the pure-Go github.com/pdfcpu/pdfcpu library, so read_file can
// hand a bounded PDF to a model without external binaries and without writing
// anything to disk.
package pdfslice

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// Limits are package-level vars so unit tests can exercise the boundaries with
// small fixtures.
var (
	// MaxUnpagedPDFPages is how many pages a PDF may have when read without a
	// pages selector.
	MaxUnpagedPDFPages = 10
	// MaxPDFPagesPerCall caps how many pages one pages selector may name.
	MaxPDFPagesPerCall = 20
	// MaxModelPDFBytes caps the PDF bytes returned to the model per read.
	MaxModelPDFBytes = 20 << 20
)

// newStatelessConfig builds a relaxed pdfcpu configuration without reading or
// creating ~/.config/pdfcpu, so read_file stays hermetic and sandbox-safe.
func newStatelessConfig() (*model.Configuration, error) {
	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil {
		return nil, err
	}
	conf.ValidationMode = model.ValidationRelaxed
	conf.WriteXRefStream = true
	return conf, nil
}

// ParsePageRange parses read_file's pages parameter: "N" selects one 1-based
// page and "A-B" selects an inclusive ascending range. An empty or
// whitespace-only spec means pages was omitted (provided false).
func ParsePageRange(spec string) (from, to int, provided bool, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, 0, false, nil
	}
	invalid := func(reason string) error {
		return fmt.Errorf("read_file: invalid pages %q: %s; use a single page or an inclusive range like \"3\" or \"1-5\" (max %d pages per call)",
			spec, reason, MaxPDFPagesPerCall)
	}

	left, right := spec, ""
	switch strings.Count(spec, "-") {
	case 0:
	case 1:
		left, right, _ = strings.Cut(spec, "-")
		if left == "" || right == "" {
			return 0, 0, true, invalid("a range needs a page on each side of the dash")
		}
	default:
		return 0, 0, true, invalid("only one dash is allowed")
	}

	from, err = strconv.Atoi(left)
	if err != nil {
		return 0, 0, true, invalid("pages must be numbers")
	}
	to = from
	if right != "" {
		to, err = strconv.Atoi(right)
		if err != nil {
			return 0, 0, true, invalid("pages must be numbers")
		}
	}
	if from < 1 || to < from {
		return 0, 0, true, invalid("pages are 1-based and a range must ascend")
	}
	if to-from+1 > MaxPDFPagesPerCall {
		return 0, 0, true, invalid(fmt.Sprintf("the range selects %d pages", to-from+1))
	}
	return from, to, true, nil
}

// SlicePDF counts raw's pages and, when spec names a range, returns a PDF
// containing exactly that range. An omitted range returns raw verbatim when the
// document is within MaxUnpagedPDFPages and MaxModelPDFBytes; otherwise it
// reports the limit that was exceeded and the range to request instead.
//
// The returned from, to, and totalPages are the canonical facts callers record
// for repeat tracking: to is clamped to the document's last page.
func SlicePDF(ctx context.Context, raw []byte, spec string) (sliced []byte, from, to, totalPages int, err error) {
	from, to, provided, err := ParsePageRange(spec)
	if err != nil {
		return nil, 0, 0, 0, err
	}

	conf, err := newStatelessConfig()
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("read_file: cannot initialize PDF config: %w", err)
	}

	totalPages, err = api.PageCount(ctx, bytes.NewReader(raw), conf)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("read_file: cannot read PDF: %w", err)
	}

	if !provided {
		if totalPages > MaxUnpagedPDFPages || len(raw) > MaxModelPDFBytes {
			return nil, 0, 0, totalPages, fmt.Errorf(
				"read_file: PDF has %d pages (%d bytes), exceeding the unpaged limit (%d pages / %d MiB). Call read_file with pages=\"1-%d\" (max %d pages per call)",
				totalPages, len(raw), MaxUnpagedPDFPages, MaxModelPDFBytes>>20,
				min(totalPages, MaxPDFPagesPerCall), MaxPDFPagesPerCall)
		}
		return raw, 1, totalPages, totalPages, nil
	}

	if from > totalPages {
		return nil, from, to, totalPages, fmt.Errorf(
			"read_file: PDF has %d pages; requested pages %q is out of range", totalPages, spec)
	}
	actualTo := min(to, totalPages)
	if from == 1 && actualTo == totalPages && len(raw) <= MaxModelPDFBytes {
		return raw, from, actualTo, totalPages, nil
	}

	var buf bytes.Buffer
	selected := []string{fmt.Sprintf("%d-%d", from, actualTo)}
	if err := api.Trim(ctx, bytes.NewReader(raw), &buf, selected, conf); err != nil {
		return nil, from, actualTo, totalPages, fmt.Errorf(
			"read_file: cannot extract PDF pages %d-%d: %w", from, actualTo, err)
	}
	if buf.Len() > MaxModelPDFBytes {
		return nil, from, actualTo, totalPages, fmt.Errorf(
			"read_file: PDF pages %d-%d are %d bytes, exceeding the %d MiB per-call cap; request a smaller page range",
			from, actualTo, buf.Len(), MaxModelPDFBytes>>20)
	}
	return buf.Bytes(), from, actualTo, totalPages, nil
}
