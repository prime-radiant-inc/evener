// Opening a document from outside its session (spec 7.1).
import { expect, it } from "vitest";
import { openDocumentInSession } from "./openDocument";

const trail = {
	sessionRef: "local:fix",
	path: "docs/superpowers/plans/settle-race.md",
	reviewRef: "local:coord",
	reviewTitle: "Get PR 2138 Test Clean",
	updatedAt: "2026-09-26T11:39:00.000Z",
};

it("opens the document inside its session, so Back lands in the session", () => {
	const pushed: [string, object][] = [];
	openDocumentInSession({ push: (name: string, params: object) => pushed.push([name, params]) } as never, {
		hubId: "studio",
		sessionRef: trail.sessionRef,
		path: trail.path,
		reviewRef: trail.reviewRef,
		reviewTitle: trail.reviewTitle,
		updatedAt: trail.updatedAt,
	});
	expect(pushed).toEqual([
		["Conversation", { hubId: "studio", ref: "local:coord", title: "Get PR 2138 Test Clean" }],
		[
			"Reader",
			{
				hubId: "studio",
				sessionRef: trail.sessionRef,
				path: trail.path,
				reviewRef: trail.reviewRef,
				reviewTitle: trail.reviewTitle,
				updatedAt: trail.updatedAt,
			},
		],
	]);
});
