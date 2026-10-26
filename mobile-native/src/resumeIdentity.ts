/** One identity-changing resume's source and resolved destination. The
 * mutations database records it in the same savepoint that moves the rows, so
 * the draft handoff obligation survives a drafts database that cannot write. */
export interface ResumeIdentityEndpoint {
	hubId: string;
	sessionRef: string;
}

export interface ResumeIdentityEdge {
	from: ResumeIdentityEndpoint;
	to: ResumeIdentityEndpoint;
}

/** What a draft library needs to reconstruct a pending handoff from the
 * mutations database when the drafts database refused the mapping. */
export interface ResumeIdentityLedger {
	readResumeIdentities(): readonly ResumeIdentityEdge[];
	/** Mappings already settled or refused. Kept here when the drafts database
	 * refused to record the settlement, so a restart retries cleanup only. */
	readSettledResumeIdentities?(): readonly ResumeIdentityEdge[];
	/** Delete the settled edge and the provenance that pointed at its source in
	 * one savepoint, so a crash cannot leave either one alone. */
	settleResumeIdentity(from: ResumeIdentityEndpoint, to: ResumeIdentityEndpoint): void;
	/** Drop a settlement once the drafts mapping it covers is gone. */
	removeResumeIdentitySettlement?(from: ResumeIdentityEndpoint, to: ResumeIdentityEndpoint): void;
	/** Record a still-pending source's redirected destination. The previous
	 * edge stays as chain provenance until the intermediate settles. */
	redirectResumeIdentity(
		from: ResumeIdentityEndpoint,
		previousTo: ResumeIdentityEndpoint,
		to: ResumeIdentityEndpoint,
	): void;
}
