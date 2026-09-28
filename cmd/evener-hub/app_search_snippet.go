package hub

import "primeradiant.com/evener/appwire"

func searchSnippet(text string, _ []string) []appwire.SearchSnippetPart {
	return []appwire.SearchSnippetPart{{Text: text}}
}
