package agent

import "primeradiant.com/evener/appwire"

// onHubObservation feeds a watch an observation as the bound serving
// generation would, on behalf of the hub watch tests.
func (jm *jobManager) onHubObservation(watchID string, update appwire.SessionWatchUpdate) error {
	return jm.onHubObservationLookup(watchID, update, false, nil)
}
