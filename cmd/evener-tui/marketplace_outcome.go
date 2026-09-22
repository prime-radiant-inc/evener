package tui

import (
	"encoding/json"
	"errors"
	"fmt"

	"primeradiant.com/evener/appwire"
)

// marketplaceRemovalState classifies the hub's post-apply marketplace
// removal rejections (appwire/errors.go) for the TUI's mutation boundary -
// the Go counterpart of the SDK's MarketplaceRemovalOutcome kinds. Every
// marker means the removal already stood on the hub, so no marked state is
// ever retryable; the caller reports what happened and reconciles instead.
type marketplaceRemovalState uint8

const (
	// marketplaceRemovalNotMarked is any rejection without a post-apply
	// marker: an ordinary failure the caller may retry.
	marketplaceRemovalNotMarked marketplaceRemovalState = iota
	// marketplaceRemovalRemoved is the hub's marketplaceRemoveApplied marker
	// (the SDK's {kind:"removed"}): the unregister and the clone cleanup
	// both completed, and only the fresh list read failed, so nothing was
	// left on disk - its account never claims litter. The hub emits the
	// marker from one site, always with AppliedUnavailable set, so the
	// marker alone is the proof; no snapshot exists to decode.
	marketplaceRemovalRemoved
	// marketplaceRemovalApplied is the marketplaceUnregisteredCloneRemains
	// marker with a decodable applied snapshot (the SDK's
	// {kind:"applied"}): the removal stood, clone files remain on disk, and
	// the snapshot is the authoritative post-removal list.
	marketplaceRemovalApplied
	// marketplaceRemovalUnavailable is the marketplaceUnregisteredCloneRemains
	// marker whose applied snapshot is missing, unavailable, or malformed
	// (the SDK's {kind:"unavailable"}): the removal stood and left clone
	// files behind, but only a fresh list read can confirm the post-removal
	// state.
	marketplaceRemovalUnavailable
)

// classifyMarketplaceRemovalOutcome accepts both the typed values used by
// local handlers and the maps decoded from a JSON-RPC error. A nil Applied
// slice is deliberately uncertain: JSON null or a missing field is not an
// empty list, and neither is a member that does not decode as a real
// marketplace row - a null or empty-object member unmarshals into a zero
// entry, and a member missing a required field, or carrying a null optional,
// unmarshals into a zero that hides the truncation - so the snapshot it sits
// in never looks authoritative.
func classifyMarketplaceRemovalOutcome(err error) (marketplaceRemovalState, appwire.MarketplaceListResponse) {
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		return marketplaceRemovalNotMarked, appwire.MarketplaceListResponse{}
	}
	if marketplaceRemovalAppliedMarker(wire.Data) {
		return marketplaceRemovalRemoved, appwire.MarketplaceListResponse{}
	}
	data, marked := decodeMarketplaceCloneRemainsData(wire.Data)
	if !marked {
		return marketplaceRemovalNotMarked, appwire.MarketplaceListResponse{}
	}
	if data.AppliedUnavailable || data.Applied.Marketplaces == nil || !validMarketplaceSnapshot(data.Applied) {
		return marketplaceRemovalUnavailable, appwire.MarketplaceListResponse{}
	}
	return marketplaceRemovalApplied, data.Applied
}

// validMarketplaceSnapshot reports whether every member of a decoded applied
// snapshot is a real marketplace row, the same trust-boundary re-check the
// SDK's classifier performs: json.Unmarshal succeeding does not make a null
// or empty-object member a row, and a snapshot carrying one must never look
// authoritative - the rule a partially decoded snapshot already follows.
// Real rows always carry a name and a source kind; an empty list is a valid
// one, since the marketplace just removed can be the last. The check is
// presence-blind by design: it runs on decoded values from both wire shapes,
// and the zero-timestamp rule that compensates for the typed path's erased
// presence lives in decodeMarketplaceCloneRemainsData's typed branch, where
// the JSON presence re-check cannot reach - a present zero that crossed the
// wire is a real timestamp and stays authoritative.
func validMarketplaceSnapshot(snapshot appwire.MarketplaceListResponse) bool {
	for _, entry := range snapshot.Marketplaces {
		if entry.Name == "" || entry.Source.Kind == "" {
			return false
		}
	}
	return true
}

// marketplaceRemovalAppliedMarker reports whether raw WireError.Data carries
// the marketplaceRemoveApplied discriminator, as the typed value the hub
// builds in-process or the map a JSON-RPC client decodes. The marker alone is
// the proof: the hub emits it from one site, always with AppliedUnavailable,
// and nothing beyond the discriminator is checked - a malformed payload must
// not demote a standing removal back to a retryable failure. The marker
// carries no snapshot to decode, so nothing else is read.
func marketplaceRemovalAppliedMarker(raw any) bool {
	if data, ok := raw.(appwire.MarketplaceRemoveAppliedData); ok {
		return data.EvenerErrorInfo == appwire.ErrorMarketplaceRemoveApplied
	}
	encoded, err := json.Marshal(raw)
	if err != nil || string(encoded) == "null" {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return false
	}
	var info appwire.ErrorInfo
	if err := json.Unmarshal(fields["evenerErrorInfo"], &info); err != nil {
		return false
	}
	return info == appwire.ErrorMarketplaceRemoveApplied
}

// decodeMarketplaceCloneRemainsData reads the marketplaceUnregisteredCloneRemains
// marker's payload, marker-first: a data that does not carry that
// discriminator is not this marker's payload at all, whatever else it holds.
func decodeMarketplaceCloneRemainsData(raw any) (appwire.MarketplaceUnregisteredCloneRemainsData, bool) {
	if data, ok := raw.(appwire.MarketplaceUnregisteredCloneRemainsData); ok {
		if data.EvenerErrorInfo != appwire.ErrorMarketplaceUnregisteredCloneRemains {
			return appwire.MarketplaceUnregisteredCloneRemainsData{}, false
		}
		// The typed payload crossed no wire, so the JSON path's presence
		// re-check never ran on its members, and decoding into the struct
		// has erased presence: a zero lastUpdated is indistinguishable
		// from the omitted field that re-check rejects. Degrade the
		// snapshot like a partial JSON one - never let it look
		// authoritative - while the JSON path keeps its own presence rule,
		// where a present zero is a real timestamp the hub really sends
		// and the snapshot stays applied.
		for _, entry := range data.Applied.Marketplaces {
			if entry.LastUpdated == 0 {
				data.Applied = appwire.MarketplaceListResponse{}
				return data, true
			}
		}
		return data, true
	}
	encoded, err := json.Marshal(raw)
	if err != nil || string(encoded) == "null" {
		return appwire.MarketplaceUnregisteredCloneRemainsData{}, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return appwire.MarketplaceUnregisteredCloneRemainsData{}, false
	}
	var info appwire.ErrorInfo
	if err := json.Unmarshal(fields["evenerErrorInfo"], &info); err != nil || info != appwire.ErrorMarketplaceUnregisteredCloneRemains {
		return appwire.MarketplaceUnregisteredCloneRemainsData{}, false
	}
	data := appwire.MarketplaceUnregisteredCloneRemainsData{EvenerErrorInfo: info}
	if rawUnavailable, ok := fields["appliedUnavailable"]; ok {
		if err := json.Unmarshal(rawUnavailable, &data.AppliedUnavailable); err != nil {
			return data, true
		}
	}
	if rawApplied, ok := fields["applied"]; ok && string(rawApplied) != "null" {
		var members struct {
			Marketplaces []json.RawMessage `json:"marketplaces"`
		}
		if err := json.Unmarshal(rawApplied, &members); err != nil {
			data.Applied = appwire.MarketplaceListResponse{}
			return data, true
		}
		for _, member := range members.Marketplaces {
			if !validMarketplaceEntryJSON(member) {
				// Never let that partial snapshot look authoritative to the caller.
				data.Applied = appwire.MarketplaceListResponse{}
				return data, true
			}
		}
		var applied appwire.MarketplaceListResponse
		if err := json.Unmarshal(rawApplied, &applied); err != nil {
			// json.Unmarshal may leave partially decoded entries behind. Never
			// let that partial snapshot look authoritative to the caller.
			data.Applied = appwire.MarketplaceListResponse{}
			return data, true
		}
		data.Applied = applied
	}
	return data, true
}

// validMarketplaceEntryJSON reports whether raw, one raw member of a decoded
// applied snapshot, carries the wire shape appwire.MarketplaceEntry
// guarantees - the exact trust-boundary re-check the SDK's classifier performs
// (appwire-client/typescript/state/extensions/marketplaces.ts,
// isMarketplaceEntry): an object - never null, never an array - whose
// required name, lastUpdated, and source are PRESENT with their JSON types
// (name a string, lastUpdated a number, source an object whose kind is a
// string), whose optional installLocation is a string when present, and whose
// source carries each present optional member as a string. Presence matters
// because decoding into the struct erases it: a missing lastUpdated
// unmarshals into a zero and a null optional into an empty string, so the
// typed decode alone cannot tell a truncated member from a whole one.
func validMarketplaceEntryJSON(raw json.RawMessage) bool {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entry); err != nil {
		return false
	}
	if !isJSONString(entry["name"]) {
		return false
	}
	if !isJSONNumber(entry["lastUpdated"]) {
		return false
	}
	if location, ok := entry["installLocation"]; ok && !isJSONString(location) {
		return false
	}
	var source map[string]json.RawMessage
	if rawSource, ok := entry["source"]; !ok || json.Unmarshal(rawSource, &source) != nil {
		return false
	}
	if !isJSONString(source["kind"]) {
		return false
	}
	for _, field := range []string{"repo", "url", "path", "ref", "sha"} {
		if rawField, ok := source[field]; ok && !isJSONString(rawField) {
			return false
		}
	}
	return true
}

// isJSONString reports whether raw is a present JSON string - not absent, not
// null, not any other type: the wire type the SDK's re-check demands of every
// field it names.
func isJSONString(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return false
	}
	_, ok := value.(string)
	return ok
}

// isJSONNumber reports whether raw is a present JSON number - not absent, not
// null, not any other type.
func isJSONNumber(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return false
	}
	switch value.(type) {
	case float64, json.Number:
		return true
	}
	return false
}

// marketplaceCloneRemainsWarning reports a removal the hub marked
// marketplaceUnregisteredCloneRemains: the removal stood and clone files
// remain on disk, so it is not retryable, and the caller reconciles from the
// marker's own snapshot or a fresh read instead.
func marketplaceCloneRemainsWarning(err error, unavailable bool) error {
	message := "marketplace removed; clone files remain on disk"
	if unavailable {
		message += "; current list could not be confirmed"
	}
	return fmt.Errorf("%s: %w", message, err)
}

// marketplaceRemovedWarning reports a removal the hub marked
// marketplaceRemoveApplied: the removal and its clone cleanup both landed,
// and only the fresh list read failed. Unlike the clone-remains marker nothing
// was left on disk, so this account never claims litter; the removal stands,
// so it is not retryable, and the fresh read the model issues settles the
// list.
func marketplaceRemovedWarning(err error) error {
	return fmt.Errorf("marketplace removed; the updated list was unavailable, so it is being refreshed: %w", err)
}
