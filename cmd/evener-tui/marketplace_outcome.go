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
// empty list.
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
	if data.AppliedUnavailable || data.Applied.Marketplaces == nil {
		return marketplaceRemovalUnavailable, appwire.MarketplaceListResponse{}
	}
	return marketplaceRemovalApplied, data.Applied
}

// marketplaceRemovalAppliedMarker reports whether raw WireError.Data carries
// the marketplaceRemoveApplied discriminator, as the typed value the hub
// builds in-process or the map a JSON-RPC client decodes. The marker alone is
// the proof: the hub emits it from one site, always with AppliedUnavailable,
// and #2068 deliberately stopped the strict data check there - a malformed
// payload must not demote a standing removal back to a retryable failure. The
// marker carries no snapshot to decode, so nothing else is read.
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
		return data, data.EvenerErrorInfo == appwire.ErrorMarketplaceUnregisteredCloneRemains
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
