package main

import (
	"bytes"
	"testing"

	"primeradiant.com/evener/internal/maketargetsdoc/makefile"
)

// FuzzRenderRewrite drives the generator's generated-region consumer over
// whatever the parser accepts: rendering the parsed targets and rewriting the
// same region twice must be byte-idempotent.
func FuzzRenderRewrite(f *testing.F) {
	f.Add([]byte("## Build the binary.\nbuild:\n\tgo build .\n"))
	f.Add([]byte("## Run all checks.\r\n## proves: Static.\r\n##   More.\r\ncheck:\n\t@true\n"))

	f.Fuzz(func(t *testing.T, src []byte) {
		if len(src) > 64<<10 {
			t.Skip()
		}
		targets, err := makefile.ParseFamily(src)
		if err != nil {
			return
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
