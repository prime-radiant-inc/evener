package agent

// Stable valid session IDs for hand-built job-manager fixtures. Distinct roles
// stay distinct so nested ownership and forwarding tests exercise the same
// topology without passing human-readable placeholders to job ID generation.
const (
	testOwnerSessionID       = "02wMz5TxvEMoJEDTDGOTil"
	testParentSessionID      = "02wMz5Txv1C3Hut0M8GCeB"
	testChildSessionID       = "02wMz5Txv2enqVTitaig6F"
	testRootSessionID        = "02wMz5Txv47YP64RR3B9YJ"
	testCoordinatorSessionID = "02wMz5Txv5aIxgf9yVdd0N"
	testWorkerSessionID      = "02wMz5Txv733WHFsVy66SR"
	testDeadSessionID        = "02wMz5Txv8Vo4rqb3QYZuV"
	testDeadChildSessionID   = "02wMz5Txv9yYdSRJat13MZ"
	testObserverID           = "02wMz5TxvBRJC3228LTWod"
)
