package appsource

import "testing"

// FuzzAppSourceProgram drives the package's deterministic appwire, loopback
// HTTP, mapping, lifecycle, and registry scenarios. The selector is deliberately
// input-driven, while the seed corpus guarantees package-complete replay.
func FuzzAppSourceProgram(f *testing.F) {
	scenarios := []func(*testing.T){
		fuzzScenarioLocalDaemonDialSeamPreservesCallerCancellation,
		fuzzScenarioLocalDaemonRemainingTransportBranches,
		fuzzScenarioForwardLocalDaemonNotificationCanceled,
		fuzzScenarioLocalDaemonInternalHandshakeErrorFallbacks,
		fuzzScenarioLocalDaemonDialErrorMapsTransportFailures,
		fuzzScenarioLocalDaemonDialErrorPassesThroughApplicationErrors,
		fuzzScenarioLocalDaemonSubscribeReadErrorPreservesApplicationWireErrors,
		fuzzScenarioLocalDaemonSubscribeReadErrorMapsInternalTransportWireErrors,
		fuzzScenarioLocalDaemonCallErrorMapsRawTransportFailures,
		fuzzScenarioLocalDaemonInitializeErrorPreservesApplicationWireErrors,
		fuzzScenarioLocalDaemonInitializeErrorMapsInternalTransportWireErrors,
		fuzzScenarioLocalDaemonCallErrorPreservesCallerCancellation,
		fuzzScenarioLocalDaemonDialErrorIgnoresNil,
		fuzzScenarioLocalDaemonSourceReadThreadMapsIOTimeoutToSessionUnavailable,
		fuzzScenarioLocalDaemonSourceReadThreadMapsEOFDuringHandshake,
		fuzzScenarioLocalDaemonSourceReadThreadReturnsCallerCtxCancellation,
		fuzzScenarioLocalDaemonSourceListsOnlyAppWireRendezvousThreads,
		fuzzScenarioLocalDaemonSourceThreadTimestampsUseStartedAtAndZeroForMissing,
		fuzzScenarioLocalDaemonSourceReadsThreadOverAppWire,
		fuzzScenarioLocalDaemonSourceJobsOverAppWire,
		fuzzScenarioLocalDaemonSourceDrainUsesInputShapeDirectly,
		fuzzScenarioLocalDaemonSourceReadThreadIncludesQueue,
		fuzzScenarioLocalDaemonSourceListQueuesOnlyProcessingThreads,
		fuzzScenarioLocalDaemonSourceListCarriesAskPending,
		fuzzScenarioLocalDaemonSourceSubscribeThreadRequestsSubscription,
		fuzzScenarioLocalDaemonSourceSubscribeThreadMapsConnectionRefused,
		fuzzScenarioLocalDaemonSourceSubscribeThreadPreservesInitializeWireError,
		fuzzScenarioLocalDaemonSourceSubscribeThreadPreservesThreadReadWireError,
		fuzzScenarioLocalDaemonSourceStartTurnMapsDroppedTransportToMutationOutcomeUnknown,
		fuzzScenarioLocalDaemonSourceSendsHubTokenBearer,
		fuzzScenarioRegistryRoutesByRefSource,
		fuzzScenarioRegistryRejectsMissingSource,
		fuzzScenarioRegistryAllReturnsSourcesInIDOrder,
		fuzzScenarioLocalDaemonSourceRPCSurface,
		fuzzScenarioLocalDaemonErrorMappingRemainingBranches,
		fuzzScenarioLocalDaemonSourceRejectsUnknownReferenceAcrossRPCSurface,
		fuzzScenarioLocalDaemonFilteringAndRegistryInvalidRef,
		fuzzScenarioLocalDaemonThreadStatus,
		fuzzScenarioFirstLocalHelpers,
		fuzzScenarioLocalThreadTimestamps,
		fuzzScenarioLocalThreadTitle,
		fuzzScenarioCompareLocalOrderText,
		fuzzScenarioLocalThreadLess,
		fuzzScenarioRegistryRemove,
	}

	for i := range scenarios {
		f.Add(uint8(i))
	}
	f.Fuzz(func(t *testing.T, selector uint8) {
		scenarios[int(selector)%len(scenarios)](t)
	})
}
