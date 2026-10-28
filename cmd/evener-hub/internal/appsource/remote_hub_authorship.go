package appsource

import (
	"encoding/json"

	"primeradiant.com/evener/appwire"
)

// Item provenance is structural metadata. Never walk text, arguments or Raw.
func (s *RemoteHubSource) translateItemAuthorship(item *appwire.ThreadItem) {
	if item.Authorship == nil {
		return
	}
	cloned := *item.Authorship
	cloned.Author.SessionRef = s.translateNestedRef(cloned.Author.SessionRef)
	cloned.Author.OwnerRef = s.translateNestedRef(cloned.Author.OwnerRef)
	item.Authorship = &cloned
}

func (s *RemoteHubSource) translateTurnItems(turn *appwire.Turn) {
	rewriteTurnImageURLs(s.id, turn)
	for i := range turn.Items {
		s.translateItemAuthorship(&turn.Items[i])
	}
}

func (s *RemoteHubSource) translateRawItemAuthorship(raw json.RawMessage) json.RawMessage {
	fields, ok := rawJSONObject(raw)
	if !ok {
		return raw
	}
	authorship, ok := rawJSONObject(fields["authorship"])
	if !ok {
		return raw
	}
	author, ok := rawJSONObject(authorship["author"])
	if !ok {
		return raw
	}
	for _, key := range []string{"sessionRef", "ownerRef"} {
		var ref string
		if value, ok := author[key]; ok && json.Unmarshal(value, &ref) == nil {
			author[key], _ = json.Marshal(s.translateNestedRef(ref))
		}
	}
	authorship["author"] = encodeRawObject(authorship["author"], author)
	fields["authorship"] = encodeRawObject(fields["authorship"], authorship)
	return encodeRawObject(raw, fields)
}

func (s *RemoteHubSource) translateRawTurnAuthorship(raw json.RawMessage) json.RawMessage {
	fields, ok := rawJSONObject(raw)
	if !ok {
		return raw
	}
	if items, ok := fields["items"]; ok {
		fields["items"] = s.rewriteRawArray(items, s.translateRawItemAuthorship)
	}
	return encodeRawObject(raw, fields)
}

func (s *RemoteHubSource) translateRawThreadAuthorship(raw json.RawMessage) json.RawMessage {
	fields, ok := rawJSONObject(raw)
	if !ok {
		return raw
	}
	if turns, ok := fields["turns"]; ok {
		fields["turns"] = s.rewriteRawArray(turns, s.translateRawTurnAuthorship)
	}
	return encodeRawObject(raw, fields)
}
