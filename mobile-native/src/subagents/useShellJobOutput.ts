import {
	ACTIVITY_REFRESH_MIN_INTERVAL_MS,
	type ActivityJob,
	type JobLogTail,
	parseJobLogTail,
} from "@evener/appwire-client";
import { useEffect, useState } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

/** A shell job's output as its detail shows it: still being read, not
 * readable (the read failed, or answered with something that isn't a tail),
 * or its tail. */
export type ShellJobOutput = { status: "reading" } | { status: "failed" } | { status: "read"; tail: JobLogTail };

const READING: ShellJobOutput = { status: "reading" };

/** How often a running job's tail is read again while its detail is in
 * front. The hub announces a job's start and finish (evener/jobs/treeUpdated)
 * but nothing as it writes, so the output is paced like the Activity list's
 * own reads. */
export const JOB_OUTPUT_REREAD_MS = ACTIVITY_REFRESH_MIN_INTERVAL_MS;

/** The tail of a shell job's output through `client`, read again whenever
 * the job wrote more or changed state, and every JOB_OUTPUT_REREAD_MS while
 * it runs and `inFront`; nothing is read while the client or job is null. The output is always the given job's: with no job, or a
 * different one, it is "reading" until that job's tail is read. A failed read
 * keeps a tail already on screen rather than blanking it. evener/jobs/output
 * answers for the session that owns the job, never the coordinator's. */
export function useShellJobOutput(
	client: ConversationClientLike | null,
	job: ActivityJob | null,
	inFront: boolean,
): ShellJobOutput {
	const ownerRef = job?.ownerRef;
	const jobId = job?.jobId;
	const outputBytes = job?.outputBytes;
	const status = job?.status;
	const terminal = job?.terminal;
	const live = inFront && terminal === false;
	// What was read is kept with the job it was read for.
	const key = job ? JSON.stringify([ownerRef, jobId]) : null;
	const [read, setRead] = useState<{ key: string; output: ShellJobOutput } | null>(null);
	// biome-ignore lint/correctness/useExhaustiveDependencies: new output or a new state is what asks for the tail again
	useEffect(() => {
		if (client === null || key === null || ownerRef === undefined || jobId === undefined) return;
		let current = true;
		let next: ReturnType<typeof setTimeout> | undefined;
		const read = (): Promise<void> =>
			client
				.request("evener/jobs/output", { ref: ownerRef, jobId })
				.then(
					(response) => {
						if (!current) return;
						const tail = parseJobLogTail(response.data);
						setRead({ key, output: tail === null ? { status: "failed" } : { status: "read", tail } });
					},
					() => {
						if (!current) return;
						setRead((shown) =>
							shown?.key === key && shown.output.status === "read" ? shown : { key, output: { status: "failed" } },
						);
					},
				)
				.finally(() => {
					if (current && live) next = setTimeout(read, JOB_OUTPUT_REREAD_MS);
				});
		void read();
		return () => {
			current = false;
			clearTimeout(next);
		};
	}, [client, key, outputBytes, status, terminal, live]);
	return read !== null && read.key === key ? read.output : READING;
}
