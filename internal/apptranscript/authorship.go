package apptranscript

import (
	"strings"

	"primeradiant.com/evener/agent/activitybound"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// ProjectAuthorship maps only recorded evidence, with no context or store lookup.
// Invalid evidence loses its author, not its independently recorded inheritance.
func ProjectAuthorship(a *schema.TranscriptAuthorship) *appwire.TranscriptAuthorship {
	if a == nil {
		return nil
	}
	out := &appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorUnknown}, Inherited: a.Inherited}
	author := a.Author
	switch author.Kind {
	case schema.TranscriptAuthorHuman, schema.TranscriptAuthorSystem, schema.TranscriptAuthorUnknown:
		if author.SessionID != "" || author.OwnerSessionID != "" || author.DelegateID != "" || author.DelegateName != "" {
			return out
		}
	case schema.TranscriptAuthorAgent:
		if !canonicalID(author.SessionID) {
			return out
		}
		if (author.OwnerSessionID == "") != (author.DelegateID == "") {
			return out
		}
		if author.OwnerSessionID != "" && (!canonicalID(author.OwnerSessionID) || !canonicalID(strings.TrimPrefix(author.DelegateID, "dlg_")) || !strings.HasPrefix(author.DelegateID, "dlg_")) {
			return out
		}
		if author.DelegateName != "" && author.DelegateID == "" {
			return out
		}
	default:
		return out
	}
	out.Author = appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorKind(author.Kind), DelegateID: author.DelegateID, DelegateName: activitybound.Truncate(author.DelegateName, activitybound.MaxLabelRunes)}
	if author.SessionID != "" {
		out.Author.SessionRef = "local:" + author.SessionID
	}
	if author.OwnerSessionID != "" {
		out.Author.OwnerRef = "local:" + author.OwnerSessionID
	}
	return out
}

func canonicalID(id string) bool {
	ref, err := appwire.ParseRef("local:" + id)
	return err == nil && ref.ThreadID == id
}
