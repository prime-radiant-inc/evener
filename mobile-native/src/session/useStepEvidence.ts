// A step's evidence, worked out again only when what it comes from changes.
// sessionRows copies every step on every publish (each streaming frame), and
// the store may rebuild a row's detail too, so the memo can't key on the
// objects. It keys on the value of every ActivityDetail field instead:
// strings and numbers as they are (compared cheaply), the object fields
// (words, tasks, a send's earlier replies) as JSON; the earlier replies are
// bounded like any reply, and rare. The whole detail goes to stepEvidence, and
// DETAIL_FIELDS must name every ActivityDetail field or the build fails, so
// no field the projection sets can be left out of the key or the evidence.
import { useMemo } from "react";
import type { ActivityDetail } from "../projectedRows";
import { type Evidence, type EvidenceSource, stepEvidence } from "./evidence";

const DETAIL_FIELDS = [
	"description",
	"summary",
	"words",
	"arguments",
	"output",
	"error",
	"exitCode",
	"durationMs",
	"callId",
	"startedAtMs",
	"endedAtMs",
	"tasks",
	"watchEvidence",
	"sendReply",
	"sendEarlierReplies",
	"sendWaitIgnored",
] as const satisfies readonly (keyof ActivityDetail)[];

// Fails to compile when ActivityDetail gains a field DETAIL_FIELDS doesn't name.
type UnkeyedField = Exclude<keyof ActivityDetail, (typeof DETAIL_FIELDS)[number]>;
const everyFieldKeyed: [UnkeyedField] extends [never] ? true : UnkeyedField = true;
void everyFieldKeyed;

// One field's part of the memo key: a string or number as it is, an object
// as its JSON, since the projection builds those afresh on every publish.
function keyPart(value: ActivityDetail[keyof ActivityDetail]): string | number | undefined {
	return typeof value === "object" ? JSON.stringify(value) : value;
}

export function useStepEvidence(step: EvidenceSource): Evidence[] {
	const { label, summaryOnly, detail } = step;
	const key = DETAIL_FIELDS.map((field) => keyPart(detail[field]));
	return useMemo(() => stepEvidence({ label, summaryOnly, detail }), [label, summaryOnly, ...key]);
}
