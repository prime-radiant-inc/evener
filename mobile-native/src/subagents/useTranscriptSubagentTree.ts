import type { ActivityTree } from "@evener/appwire-client";
import { useEffect, useRef } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { type SubagentTreeTarget, useHeldSubagentTree } from "./useHeldSubagentTree";

/** The coordinator's tree a transcript's finished subagent rows read their
 * outcomes from (spec 8.2), or null. The screen holds it, never a row: the
 * list unmounts rows that scroll off, and a tree no one holds stops reading,
 * so a row-held tree would read again each time one scrolled back. `target`
 * is set only while the transcript has a finished subagent row.
 *
 * The shared activity owner follows the coordinator independently of the
 * transcript's selected session. A transcript that also asks for a focus
 * refresh releases no other holder's collection or thread membership. */
export function useTranscriptSubagentTree(
	hubId: string,
	target: SubagentTreeTarget | null,
	client: ConversationClientLike | null,
	{ inFront, receivesUpdates }: { inFront: boolean; receivesUpdates: boolean },
): ActivityTree | null {
	const held = useHeldSubagentTree(hubId, target, client);
	const tree = held?.tree;
	const wasInFront = useRef(inFront);
	useEffect(() => {
		const cameToFront = inFront && !wasInFront.current;
		wasInFront.current = inFront;
		if (cameToFront && !receivesUpdates && tree) void tree.reload();
	}, [inFront, receivesUpdates, tree]);
	return held?.snapshot.tree ?? null;
}

/** The coordinator tree a transcript holds for its finished subagent rows:
 * none while no subagent row has finished; the session's own on a
 * coordinator's screen; and on a subagent's screen, the coordinator's under
 * the thread its panel reads now (`panelThread`), and none until the panel
 * has read it. The binding uses the stable ref; its shared context identifies
 * a replacement resolved session independently of the route's old thread hint. */
export function transcriptTreeTarget({
	showsFinished,
	coordinator,
	onSubagentScreen,
	panelThread,
}: {
	showsFinished: boolean;
	coordinator: SubagentTreeTarget | null;
	onSubagentScreen: boolean;
	panelThread: string | null;
}): SubagentTreeTarget | null {
	if (!showsFinished || !coordinator) return null;
	if (!onSubagentScreen) return { ref: coordinator.ref, threadId: coordinator.threadId };
	return panelThread ? { ref: coordinator.ref, threadId: panelThread } : null;
}
