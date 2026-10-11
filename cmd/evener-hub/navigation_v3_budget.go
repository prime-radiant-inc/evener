package hub

import (
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

type navigationV3ResponseInvariantError struct {
	kind     navigationResourceKind
	bytes    int
	maxBytes int
}

func (err navigationV3ResponseInvariantError) Error() string {
	return fmt.Sprintf("navigation v3 response invariant: minimal %s snapshot response is %d bytes, maximum is %d", err.kind, err.bytes, err.maxBytes)
}

func navigationV3ResponseLimit(kind navigationResourceKind) int {
	switch kind {
	case navigationResourceManifest:
		return maxNavigationManifestBytes
	case navigationResourcePinCatalog, navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns:
		return maxNavigationCatalogBytes
	default:
		return maxNavigationResponseBytes
	}
}

// fitNavigationV3Snapshot fits the logical projector result against the exact
// serialized snapshot response. It returns only a normalized graph which has
// passed that full-envelope check, so callers cannot accidentally remember an
// unbounded authority in delta history.
func fitNavigationV3Snapshot(
	key navigationResourceKey,
	object any,
	response appwire.NavigationReadResponse,
	maxBytes int,
) (hubapi.NavigationSnapshot, json.RawMessage, error) {
	type candidateResult struct {
		snapshot hubapi.NavigationSnapshot
		data     json.RawMessage
		bytes    int
		object   any
	}
	// encoding/json writes a RawMessage that is already json.Marshal output
	// byte for byte, so every candidate's response is exactly this envelope's
	// bytes plus its snapshot data: the envelope is encoded once, not per probe.
	envelope := response
	envelope.Representation = appwire.NavigationRepresentationSnapshot
	envelope.Base = nil
	envelope.Data = json.RawMessage(`{}`)
	encodedEnvelope, err := navigationEnvelopeMarshal(envelope)
	if err != nil {
		return hubapi.NavigationSnapshot{}, nil, fmt.Errorf("encode navigation v3 response: %w", err)
	}
	envelopeBytes := len(encodedEnvelope) - len(envelope.Data)
	probe := func(candidate any) (candidateResult, error) {
		snapshot, err := normalizeNavigationResource(key, candidate)
		if err != nil {
			return candidateResult{}, err
		}
		data, err := navigationEnvelopeMarshal(snapshot)
		if err != nil {
			return candidateResult{}, fmt.Errorf("encode navigation v3 snapshot: %w", err)
		}
		return candidateResult{snapshot: snapshot, data: data, bytes: envelopeBytes + len(data), object: candidate}, nil
	}

	initial, err := probe(object)
	if err != nil {
		return hubapi.NavigationSnapshot{}, nil, err
	}
	if err := validateNavigationPageProgress(key.Kind, initial.object); err != nil {
		return hubapi.NavigationSnapshot{}, nil, err
	}
	if initial.bytes <= maxBytes {
		return initial.snapshot, initial.data, nil
	}

	fitCandidates := func(nodes int, candidate func(int) any) (hubapi.NavigationSnapshot, json.RawMessage, error) {
		minimal, err := probe(candidate(0))
		if err != nil {
			return hubapi.NavigationSnapshot{}, nil, err
		}
		if minimal.bytes > maxBytes {
			return hubapi.NavigationSnapshot{}, nil, navigationV3ResponseInvariantError{kind: key.Kind, bytes: minimal.bytes, maxBytes: maxBytes}
		}
		fitted, bestBudget := minimal, 0
		_, err = navigationFittingBudget(nodes, maxBytes, initial.bytes, func(budget int) (int, error) {
			result, err := probe(candidate(budget))
			if err == nil && result.bytes <= maxBytes && budget > bestBudget {
				fitted, bestBudget = result, budget
			}
			return result.bytes, err
		})
		if err != nil {
			return hubapi.NavigationSnapshot{}, nil, err
		}
		if err := validateNavigationPageProgress(key.Kind, fitted.object); err != nil {
			return hubapi.NavigationSnapshot{}, nil, err
		}
		return fitted.snapshot, fitted.data, nil
	}

	switch value := object.(type) {
	case hubapi.NavigationSectionResource:
		original := cloneNavigationSummaries(value.Sessions)
		baseRemaining := value.Remaining
		return fitCandidates(navigationSummaryNodes(original), func(budget int) any {
			candidate := value
			candidate.Sessions, _ = limitNavigationSummaries(original, budget)
			candidate.Remaining = baseRemaining + len(original) - len(candidate.Sessions)
			candidate.Truncated = true
			return candidate
		})
	case hubapi.NavigationPinSectionCatalog:
		original := append(hubapi.NavigationArray[hubapi.NavigationPinSectionDescriptor](nil), value.PinSections...)
		baseRemaining := value.Remaining
		return fitCandidates(len(original), func(budget int) any {
			candidate := value
			candidate.PinSections = append(hubapi.NavigationArray[hubapi.NavigationPinSectionDescriptor](nil), original[:budget]...)
			candidate.Remaining = baseRemaining + len(original) - budget
			return candidate
		})
	case hubapi.NavigationProjectCatalog:
		original := append(hubapi.NavigationArray[hubapi.NavigationProjectSummary](nil), value.Projects...)
		baseRemaining := value.Remaining
		return fitCandidates(len(original), func(budget int) any {
			candidate := value
			candidate.Projects = append(hubapi.NavigationArray[hubapi.NavigationProjectSummary](nil), original[:budget]...)
			candidate.Remaining = baseRemaining + len(original) - budget
			return candidate
		})
	case hubapi.NavigationProjectResource:
		original := cloneNavigationProjectResource(value)
		nodes := navigationSummaryNodes(original.Current.Sessions) + navigationSummaryNodes(original.Recent.Sessions) + navigationSummaryNodes(original.Archived.Sessions)
		return fitCandidates(nodes, func(budget int) any {
			return limitNavigationProject(original, budget)
		})
	case hubapi.NavigationProjectPage:
		original := cloneNavigationSummaries(value.Sessions)
		baseRemaining := value.Remaining
		return fitCandidates(navigationSummaryNodes(original), func(budget int) any {
			candidate := value
			candidate.Sessions, _ = limitNavigationSummaries(original, budget)
			candidate.Remaining = baseRemaining + len(original) - len(candidate.Sessions)
			candidate.Truncated = true
			return candidate
		})
	case hubapi.NavigationSessionLocation:
		// A deep link requires its compact session summary. The progress check
		// rejects an envelope-only candidate when that row cannot fit.
		if value.Session == nil {
			return hubapi.NavigationSnapshot{}, nil, navigationV3ResponseInvariantError{kind: key.Kind, bytes: initial.bytes, maxBytes: maxBytes}
		}
		original := cloneNavigationSummary(*value.Session)
		return fitCandidates(navigationSummaryNodes(hubapi.NavigationArray[hubapi.NavigationSessionSummary]{original}), func(budget int) any {
			candidate := value
			if budget == 0 {
				candidate.Session = nil
				return candidate
			}
			rows := hubapi.NavigationArray[hubapi.NavigationSessionSummary]{cloneNavigationSummary(original)}
			candidate.Session = &rows[0]
			return candidate
		})
	default:
		return hubapi.NavigationSnapshot{}, nil, navigationV3ResponseInvariantError{kind: key.Kind, bytes: initial.bytes, maxBytes: maxBytes}
	}
}

func navigationV3ResponseFits(response appwire.NavigationReadResponse, maxBytes int) (bool, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return false, fmt.Errorf("encode navigation v3 response: %w", err)
	}
	return len(encoded) <= maxBytes, nil
}
