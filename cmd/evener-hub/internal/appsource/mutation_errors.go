package appsource

import "primeradiant.com/evener/appwire"

// mutationOutcomeUnknown is the internal-error refusal for a mutation whose
// response was lost: it may have applied, and disposition says whether the
// caller replays it automatically or waits for receipt reconciliation.
// clientMutationID is empty for a mutation that carries none.
func mutationOutcomeUnknown(message, clientMutationID string, disposition appwire.RetryDisposition) appwire.WireError {
	return appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: message,
		Data: appwire.ErrorData{
			EvenerErrorInfo:  appwire.ErrorMutationOutcomeUnknown,
			ClientMutationID: clientMutationID,
			MutationOutcome:  appwire.MutationOutcomeUnknown,
			RetryDisposition: disposition,
		},
	}
}

// remoteHubUnavailable is the host-unavailable refusal for the remote hub
// registered as id. detail says why, and is left out when empty.
func remoteHubUnavailable(id, detail string) appwire.WireError {
	message := "remote hub unavailable: " + id
	if detail != "" {
		message += ": " + detail
	}
	return appwire.SessionUnavailable(message)
}
