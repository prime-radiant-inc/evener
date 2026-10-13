// Opening a document from outside its session (spec 7.1).
import { expect, it } from "vitest";
import { openDocumentInSession } from "./openDocument";

const trail = {
	sessionRef: "local:fix",
	path: "docs/superpowers/plans/settle-race.md",
	reference: {
		path: "docs/superpowers/plans/settle-race.md",
		cwd: "/work/owner",
		readTarget: "/work/owner/docs/superpowers/plans/settle-race.md",
		provenance: "relative" as const,
	},
	sessionTitle: "Fix race",
	updatedAt: "2026-09-26T11:39:00.000Z",
};

it("opens the document inside its session, so Back lands in the session", () => {
	const pushed: [string, object][] = [];
	openDocumentInSession({ push: (name: string, params: object) => pushed.push([name, params]) } as never, {
		hubId: "studio",
		sessionRef: trail.sessionRef,
		path: trail.path,
		reference: trail.reference,
		sessionTitle: trail.sessionTitle,
		updatedAt: trail.updatedAt,
	});
	expect(pushed).toEqual([
		["Conversation", { hubId: "studio", ref: "local:fix", title: "Fix race" }],
		[
			"Reader",
			{
				hubId: "studio",
				sessionRef: trail.sessionRef,
				path: trail.path,
				reference: trail.reference,
				sessionTitle: trail.sessionTitle,
				updatedAt: trail.updatedAt,
			},
		],
	]);
});
