import { type JobLogTail, parseJobLogTail } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

/** A shell job's output as its detail shows it: still being read, not
 * readable (the read failed, or answered with something that isn't a tail),
 * or its tail. */
export type ShellJobOutput = { status: "reading" } | { status: "failed" } | { status: "read"; tail: JobLogTail };

/** Which job's output to read: the session that owns it (evener/jobs/output
 * answers for the owner's ref, never the coordinator's), its id, and a
 * revision that changes whenever the job wrote more or changed state, which
 * is when the tail is read again. */
export interface ShellJobOutputTarget {
	ownerRef: string;
	jobId: string;
	revision: string;
}

/** The tail of a shell job's output through `client`, read when the target's
 * revision changes; nothing is read while the client or target is null. A
 * failed read keeps a tail already on screen rather than blanking it. */
export function useShellJobOutput(
	client: ConversationClientLike | null,
	target: ShellJobOutputTarget | null,
): ShellJobOutput {
	const [output, setOutput] = useState<ShellJobOutput>({ status: "reading" });
	const ownerRef = target?.ownerRef;
	const jobId = target?.jobId;
	const revision = target?.revision;
	// biome-ignore lint/correctness/useExhaustiveDependencies: a new revision is what asks for the tail again
	useEffect(() => {
		if (client === null || ownerRef === undefined || jobId === undefined) return;
		let current = true;
		client.request("evener/jobs/output", { ref: ownerRef, jobId }).then(
			(response) => {
				if (!current) return;
				const tail = parseJobLogTail(response.data);
				setOutput(tail === null ? { status: "failed" } : { status: "read", tail });
			},
			() => {
				if (current) setOutput((shown) => (shown.status === "read" ? shown : { status: "failed" }));
			},
		);
		return () => {
			current = false;
		};
	}, [client, ownerRef, jobId, revision]);
	return output;
}
