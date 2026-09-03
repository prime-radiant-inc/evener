package hub

import (
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

type navigationV2ResponseInvariantError struct {
	kind     navigationResourceKind
	bytes    int
	maxBytes int
}

func (err navigationV2ResponseInvariantError) Error() string {
	return fmt.Sprintf("navigation v2 response invariant: minimal %s snapshot response is %d bytes, maximum is %d", err.kind, err.bytes, err.maxBytes)
}

func navigationV2ResponseLimit(kind navigationResourceKind) int {
	switch kind {
	case navigationResourceManifest:
		return maxNavigationManifestBytes
	case navigationResourcePinCatalog, navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns:
		return maxNavigationCatalogBytes
	default:
		return maxNavigationResponseBytes
	}
}

// fitNavigationV2Snapshot fits the logical projector result against the exact
// serialized snapshot response. It returns only a normalized graph which has
// passed that full-envelope check, so callers cannot accidentally remember an
// unbounded authority in delta history.
func fitNavigationV2Snapshot(
	key navigationResourceKey,
	object any,
	response appwire.NavigationReadResponse,
	maxBytes int,
) (hubapi.NavigationSnapshot, json.RawMessage, error) {
	type candidateResult struct {
		snapshot hubapi.NavigationSnapshot
		data     json.RawMessage
		bytes    int
	}
	probe := func(candidate any) (candidateResult, error) {
		snapshot, err := normalizeNavigationResource(key, candidate)
		if err != nil {
			return candidateResult{}, err
		}
		data, err := json.Marshal(snapshot)
		if err != nil {
			return candidateResult{}, fmt.Errorf("encode navigation v2 snapshot: %w", err)
		}
		candidateResponse := response
		candidateResponse.Representation = appwire.NavigationRepresentationSnapshot
		candidateResponse.Base = nil
		candidateResponse.Data = data
		encoded, err := json.Marshal(candidateResponse)
		if err != nil {
			return candidateResult{}, fmt.Errorf("encode navigation v2 response: %w", err)
		}
		return candidateResult{snapshot: snapshot, data: data, bytes: len(encoded)}, nil
	}

	initial, err := probe(object)
	if err != nil {
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
			return hubapi.NavigationSnapshot{}, nil, navigationV2ResponseInvariantError{kind: key.Kind, bytes: minimal.bytes, maxBytes: maxBytes}
		}
		fitted := minimal
		_, err = navigationFittingBudgetChecked(nodes, func(budget int) (bool, error) {
			result, probeErr := probe(candidate(budget))
			if probeErr == nil && result.bytes <= maxBytes {
				fitted = result
			}
			return result.bytes <= maxBytes, probeErr
		})
		if err != nil {
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
		return fitCandidates(nodes, func(budget int) any { return limitNavigationProject(original, budget) })
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
	default:
		return hubapi.NavigationSnapshot{}, nil, navigationV2ResponseInvariantError{kind: key.Kind, bytes: initial.bytes, maxBytes: maxBytes}
	}
}

func navigationFittingBudgetChecked(nodes int, fits func(int) (bool, error)) (int, error) {
	low, high := 0, nodes+1
	for high-low > 1 {
		middle := low + (high-low)/2
		fit, err := fits(middle)
		if err != nil {
			return 0, err
		}
		if fit {
			low = middle
		} else {
			high = middle
		}
	}
	return low, nil
}

func navigationV2ResponseFits(response appwire.NavigationReadResponse, maxBytes int) (bool, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return false, fmt.Errorf("encode navigation v2 response: %w", err)
	}
	return len(encoded) <= maxBytes, nil
}
