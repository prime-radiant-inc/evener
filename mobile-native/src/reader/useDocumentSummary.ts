// A document's summary for a chip or a Files row, read through the hub the
// connection knows (documentSummaries.ts holds the shared cache).
import { useEffect, useState } from "react";
import { useConnection } from "../ConnectionProvider";
import { type DocumentSummary, knownSummary, summaryEntry } from "./documentSummaries";

/** The document's summary, or null until its read lands (or when it failed). */
export function useDocumentSummary(
	hubId: string,
	sessionRef: string,
	path: string,
	updatedAt?: string,
): DocumentSummary | null {
	const { profiles } = useConnection();
	const origin = profiles.find((profile) => profile.id === hubId)?.origin ?? "";
	// A summary already read shows on the first render, so a list of chips
	// doesn't flash file names as it scrolls.
	const [summary, setSummary] = useState<DocumentSummary | null>(
		() => knownSummary(hubId, sessionRef, path, updatedAt),
	);
	useEffect(() => {
		// Until the hub's profile loads there's nowhere to read from, and a read
		// against no origin would stand in for the real one.
		if (origin === "") return;
		const entry = summaryEntry(hubId, origin, sessionRef, path, updatedAt);
		setSummary(entry.summary ?? null);
		if (entry.summary) return;
		let current = true;
		void entry.read.then((next) => {
			if (current) setSummary(next);
		});
		return () => {
			current = false;
		};
	}, [hubId, origin, sessionRef, path, updatedAt]);
	return summary;
}
