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
	removeResumeIdentity(from: ResumeIdentityEndpoint, to: ResumeIdentityEndpoint): void;
}
