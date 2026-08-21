package main

import (
	"bytes"
	"reflect"
	"testing"
)

// FuzzParseFamily drives the annotation grammar and its generated-region
// consumer together. Beyond the no-panic floor, identical input must parse
// deterministically; failed parses return no partial targets; successful
// targets always carry the fields the renderer requires; and rendering then
// rewriting the same region twice must be byte-idempotent.
func FuzzParseFamily(f *testing.F) {
	seeds := [][]byte{
		{},
		[]byte("## Build the binary.\nbuild:\n\tgo build .\n"),
		[]byte("## Run all checks.\r\n## proves: Static and dynamic checks.\r\n## trigger: Required CI.\r\n## requires: Go.\r\n## fails-when: Any check fails.\r\n##   The command exits nonzero.\r\ncheck:\r\n\t@true\r\n"),
		[]byte("## Text <!-- END GENERATED --> stays prose.\n##   Even beside marker text.\nbuild|`test:\n\t@true\n"),
		[]byte("## Summary.\n## unknown: value\nbuild:\n\t@true\n"),
		[]byte("##trigger: missing space\nbuild:\n\t@true\n"),
		[]byte("## Detached summary.\n\nbuild:\n\t@true\n"),
		[]byte("## Wrong attachment.\ninstall: PREFIX := /usr/local\ninstall:\n\t@true\n"),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, src []byte) {
		if len(src) > 64<<10 {
			t.Skip()
		}

		targets, err := ParseFamily(src)
		again, againErr := ParseFamily(src)
		if err != nil {
			if targets != nil {
				t.Fatalf("failed parse returned partial targets: %+v", targets)
			}
			if againErr == nil || err.Error() != againErr.Error() {
				t.Fatalf("failed parse is non-deterministic: first=%v second=%v", err, againErr)
			}
			return
		}
		if againErr != nil {
			t.Fatalf("second parse of identical input failed: %v", againErr)
		}
		if !reflect.DeepEqual(targets, again) {
			t.Fatalf("parse is non-deterministic:\nfirst:  %+v\nsecond: %+v", targets, again)
		}
		for _, target := range targets {
			if target.Name == "" || target.Summary == "" {
				t.Fatalf("successful parse returned incomplete target: %+v", target)
			}
		}

		body := Render(targets)
		doc := []byte("before\n" + beginMarker("fuzz") + endMarker + "\nafter\n")
		first, rewriteErr := RewriteRegion(doc, "fuzz", body)
		if rewriteErr != nil {
			t.Fatalf("rewrite parsed targets: %v", rewriteErr)
		}
		second, rewriteErr := RewriteRegion(first, "fuzz", body)
		if rewriteErr != nil {
			t.Fatalf("rewrite parsed targets twice: %v", rewriteErr)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("rendered rewrite is not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
		}
	})
}
