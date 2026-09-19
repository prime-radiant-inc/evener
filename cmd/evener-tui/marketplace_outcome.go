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
	data, ok := decodeMarketplaceCloneRemainsData(wire.Data)
	if !ok || data.EvenerErrorInfo != appwire.ErrorMarketplaceUnregisteredCloneRemains {
		return marketplaceCloneRemainsNotTyped, appwire.MarketplaceListResponse{}
	}
	if data.AppliedUnavailable || data.Applied.Marketplaces == nil {
		return marketplaceCloneRemainsUnavailable, appwire.MarketplaceListResponse{}
	}
	return marketplaceCloneRemainsApplied, data.Applied
}

func decodeMarketplaceCloneRemainsData(raw any) (appwire.MarketplaceUnregisteredCloneRemainsData, bool) {
	if data, ok := raw.(appwire.MarketplaceUnregisteredCloneRemainsData); ok {
		return data, true
	}
	encoded, err := json.Marshal(raw)
	if err != nil || string(encoded) == "null" {
		return appwire.MarketplaceUnregisteredCloneRemainsData{}, false
	}
	var data appwire.MarketplaceUnregisteredCloneRemainsData
	if err := json.Unmarshal(encoded, &data); err != nil {
		return appwire.MarketplaceUnregisteredCloneRemainsData{}, false
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
