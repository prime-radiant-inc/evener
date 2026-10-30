package memorypages

import (
	"errors"
	"testing"
	"time"
)

func TestPageFromFieldsAssemblesFrontmatter(t *testing.T) {
	t.Parallel()
	page, err := PageFromFields(
		"When code stores, computes or prints a money amount: any of them",
		"# Money is integer cents\n\nMoney is integer cents.\n\n**Why:** a rounding bug.\n",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nwhen: 'When code stores, computes or prints a money amount: any of them'\n---\n\n# Money is integer cents\n\nMoney is integer cents.\n\n**Why:** a rounding bug.\n"
	if page != want {
		t.Fatalf("got %q\nwant %q", page, want)
	}
	p := ParsePage("cents.md", []byte(page), time.Time{})
	if p.Unreadable || p.When != "When code stores, computes or prints a money amount: any of them" {
		t.Fatalf("page reads back as %+v", p)
	}
}

func TestPageFromFieldsRequiresWhen(t *testing.T) {
	t.Parallel()
	for _, when := range []string{"", "  \n"} {
		_, err := PageFromFields(when, "# T\nfact\n")
		if !errors.Is(err, errMemoryWriteNeedsWhen) {
			t.Fatalf("when %q: err = %v", when, err)
		}
	}
}

func TestPageFromFieldsRefusesFrontmatterInBody(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"---\ndescription: d\n---\nfact\n", "\n---\nwhen: w\n---\n"} {
		_, err := PageFromFields("w", body)
		if !errors.Is(err, errMemoryBodyFrontmatter) {
			t.Fatalf("body %q: err = %v", body, err)
		}
	}
	// A horizontal rule is not a frontmatter block, however it opens the body.
	for _, body := range []string{"# T\nfact\n\n---\n\nmore\n", "----\n# T\nfact\n", "---\n# T\nfact\n"} {
		if _, err := PageFromFields("w", body); err != nil {
			t.Fatalf("body %q was refused: %v", body, err)
		}
	}
}
