// Package textutil holds small string helpers used by the docs site.
package textutil

import "strings"

// Initials returns the first letter of each word, upper-cased.
func Initials(s string) string {
	var b strings.Builder
	for _, w := range strings.Fields(s) {
		b.WriteString(strings.ToUpper(w[:1]))
	}
	return b.String()
}
