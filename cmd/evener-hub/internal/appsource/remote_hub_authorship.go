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
	copy := *item.Authorship
	copy.Author.SessionRef = s.translateNestedRef(copy.Author.SessionRef)
	copy.Author.OwnerRef = s.translateNestedRef(copy.Author.OwnerRef)
	item.Authorship = &copy
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
