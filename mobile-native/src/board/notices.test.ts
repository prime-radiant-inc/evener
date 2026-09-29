import type { HubNotice, Source } from "@evener/appwire-client";
import { expect, it } from "vitest";
import { notices } from "./notices";

// A hub notice (S11) as evener/notices/list carries it.
const notice = (kind: string, subject: string, over: Partial<HubNotice> = {}): HubNotice => ({
	id: `${kind}:${subject}`,
	kind,
	subject,
	...over,
});
const source = (id: string, label: string, online: boolean): Source => ({ id, label, kind: "ssh", online });

it("says nothing when the hub has no notices", () => {
	expect(notices({ hubNotices: [], sources: [source("laptop", "Laptop", true)] })).toEqual([]);
});

it("names each provider whose sign-in expired, with the sessions it blocks when the hub counts them", () => {
	expect(
		notices({
			hubNotices: [
				notice("signInRequired", "codex-jesse-fsck.com", { affectedSessions: 3 }),
				notice("signInRequired", "openai"),
			],
			sources: [],
		}),
	).toEqual([
		{
			key: "signInRequired:codex-jesse-fsck.com",
			kind: "signIn",
			text: "codex-jesse-fsck.com sign-in expired · 3 sessions",
			action: "Sign in",
			providerId: "codex-jesse-fsck.com",
		},
		{
			key: "signInRequired:openai",
			kind: "signIn",
			text: "openai sign-in expired",
			action: "Sign in",
			providerId: "openai",
		},
	]);
});

it("names an offline host by its label from the manifest, with the hub's count", () => {
	expect(
		notices({
			hubNotices: [
				notice("hostOffline", "studio", { affectedSessions: 1 }),
				notice("hostOffline", "rack", { affectedSessions: 2 }),
			],
			sources: [source("studio", "Studio Mac", false)],
		}),
	).toEqual([
		{
			key: "hostOffline:studio",
			kind: "host",
			text: "Studio Mac is offline · 1 session",
			action: "Details",
			sourceId: "studio",
		},
		// A host the manifest doesn't list yet goes by its id.
		{
			key: "hostOffline:rack",
			kind: "host",
			text: "rack is offline · 2 sessions",
			action: "Details",
			sourceId: "rack",
		},
	]);
});

it("names each broken plugin, and its marketplace when two broken plugins share a name", () => {
	expect(
		notices({
			hubNotices: [
				{ ...notice("pluginBroken", "superpowers"), id: "pluginBroken:superpowers@evener", marketplace: "evener" },
				{
					...notice("pluginBroken", "superpowers"),
					id: "pluginBroken:superpowers@community",
					marketplace: "community",
				},
				{ ...notice("pluginBroken", "go"), id: "pluginBroken:go@evener", marketplace: "evener" },
			],
			sources: [],
		}),
	).toEqual([
		{
			key: "pluginBroken:superpowers@evener",
			kind: "plugin",
			text: "superpowers from evener is broken",
			action: "Plugins",
			pluginId: "superpowers",
			marketplace: "evener",
		},
		{
			key: "pluginBroken:superpowers@community",
			kind: "plugin",
			text: "superpowers from community is broken",
			action: "Plugins",
			pluginId: "superpowers",
			marketplace: "community",
		},
		{
			key: "pluginBroken:go@evener",
			kind: "plugin",
			text: "go is broken",
			action: "Plugins",
			pluginId: "go",
			marketplace: "evener",
		},
	]);
});

it("keeps the hub's order, and leaves out a kind it doesn't know", () => {
	expect(
		notices({
			hubNotices: [
				notice("signInRequired", "openai"),
				notice("somethingNew", "x"),
				notice("hostOffline", "studio"),
				{ ...notice("pluginBroken", "go"), marketplace: "evener" },
			],
			sources: [],
		}).map((found) => found.kind),
	).toEqual(["signIn", "host", "plugin"]);
});
