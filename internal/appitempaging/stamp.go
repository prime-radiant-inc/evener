package appitempaging

import (
	"encoding/json"

	"primeradiant.com/evener/appwire"
)

// OmitResolvedAskCalls returns the item with its per-call resolution stamp
// omitted as a UNIT when that list alone exceeds limit, reporting whether
// anything changed (#6323).
//
// The call list is optional metadata, not a body: reply text, the turn links
// and the legacy count all stay, so a page that is over budget only because of
// the list can be served instead of dropping an item or refusing the read. The
// observation is copied before editing, so bounding a read never rewrites the
// canonical item a caller retains.
func OmitResolvedAskCalls(item appwire.ThreadItem, limit int) (appwire.ThreadItem, bool) {
	observation := item.Observation
	if observation == nil || len(observation.ResolvedAskCalls) == 0 {
		return item, false
	}
	// A zero limit means "omit it", which is what the recovery paths want: they
	// run on a page that is already over budget, so measuring the list before
	// discarding it would spend the encoding cost at the worst moment.
	if limit > 0 {
		encoded, err := json.Marshal(observation.ResolvedAskCalls)
		if err != nil || len(encoded) <= limit {
			return item, false
		}
	}
	bounded := *observation
	bounded.ResolvedAskCalls = nil
	item.Observation = &bounded
	return item, true
}
