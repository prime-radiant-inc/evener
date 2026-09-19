package tui

import (
	"encoding/json"
	"errors"
	"fmt"

	"primeradiant.com/evener/appwire"
)

type marketplaceCloneRemainsState uint8

const (
	marketplaceCloneRemainsNotTyped marketplaceCloneRemainsState = iota
	marketplaceCloneRemainsApplied
	marketplaceCloneRemainsUnavailable
)

// classifyMarketplaceCloneRemains accepts both the typed value used by local
// handlers and the map decoded from a JSON-RPC error. A nil Applied slice is
// deliberately uncertain: JSON null or a missing field is not an empty list.
func classifyMarketplaceCloneRemains(err error) (marketplaceCloneRemainsState, appwire.MarketplaceListResponse) {
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		return marketplaceCloneRemainsNotTyped, appwire.MarketplaceListResponse{}
	}
	data, marked := decodeMarketplaceCloneRemainsData(wire.Data)
	if !marked {
		return marketplaceCloneRemainsNotTyped, appwire.MarketplaceListResponse{}
	}
	if data.AppliedUnavailable || data.Applied.Marketplaces == nil {
		return marketplaceCloneRemainsUnavailable, appwire.MarketplaceListResponse{}
	}
	return marketplaceCloneRemainsApplied, data.Applied
}

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

func marketplaceCloneRemainsWarning(err error, unavailable bool) error {
	message := "marketplace removed; clone files remain on disk"
	if unavailable {
		message += "; current list could not be confirmed"
	}
	return fmt.Errorf("%s: %w", message, err)
}
