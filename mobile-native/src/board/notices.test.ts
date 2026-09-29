import type { AuthStatusResponse, NavigationSessionSummary, PluginEntry, Source } from "@evener/appwire-client";
import { expect, it } from "vitest";
import { notices } from "./notices";

const provider = (name: string, over: Partial<AuthStatusResponse> = {}): AuthStatusResponse => ({
	provider: name,
	supported: true,
	signedIn: true,
	activeSource: "oauth",
	hasStoredOAuth: true,
	...over,
});
const plugin = (name: string, broken: boolean): PluginEntry => ({
	plugin: name,
	marketplace: "evener",
	version: "1.0.0",
	enabled: true,
	autoUpgrade: false,
	broken,
	installPath: `/plugins/${name}`,
	installedAt: 0,
	lastUpdated: 0,
});
const source = (id: string, label: string, online: boolean): Source => ({ id, label, kind: "ssh", online });
const row = (ref: string, hostId: string): NavigationSessionSummary => ({
	ref,
	host_id: hostId,
	session_id: ref,
	title: ref,
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
});
const none = { auth: [], sources: [], plugins: [], loadedRows: [] };

it("says nothing when every provider is signed in, every host online and every plugin healthy", () => {
	expect(
		notices({
			auth: [provider("anthropic"), provider("openai", { needsLogin: false, needsRefresh: true })],
			sources: [source("laptop", "Laptop", true)],
			plugins: [plugin("superpowers", false)],
			loadedRows: [row("laptop:a", "laptop")],
		}),
	).toEqual([]);
});

it("names each provider whose sign-in expired, with the provider as the hub spells it", () => {
	expect(
		notices({
			...none,
			auth: [
				provider("anthropic"),
				provider("codex-jesse-fsck.com", { needsLogin: true }),
				provider("openai", { needsLogin: true }),
			],
		}),
	).toEqual([
		{
			key: "signIn:codex-jesse-fsck.com",
			kind: "signIn",
			text: "codex-jesse-fsck.com sign-in expired",
			action: "Sign in",
			providerId: "codex-jesse-fsck.com",
		},
		{ key: "signIn:openai", kind: "signIn", text: "openai sign-in expired", action: "Sign in", providerId: "openai" },
	]);
});

// Spec 7.1: "one sentence naming the affected count". A session the
// provider's expired sign-in stopped is a failed row naming that provider.
it("counts the loaded sessions an expired sign-in stopped, each once, and says nothing of a count of none", () => {
	const stopped = (ref: string, by: string): NavigationSessionSummary => ({
		...row(ref, "laptop"),
		state: "failed",
		failure: { title: `${by} sign-in expired (401)`, cause_kind: "provider", provider: by, status: 401 },
	});
	const codex = [stopped("laptop:a", "codex-jesse-fsck.com"), stopped("laptop:b", "codex-jesse-fsck.com")];
	expect(
		notices({
			...none,
			auth: [provider("codex-jesse-fsck.com", { needsLogin: true }), provider("openai", { needsLogin: true })],
			// Live's rows, then Needs you's: laptop:a is in both.
			loadedRows: [...codex, stopped("laptop:c", "anthropic"), codex[0]],
		}).map((notice) => notice.text),
	).toEqual(["codex-jesse-fsck.com sign-in expired · 2 sessions", "openai sign-in expired"]);
});

it("counts an offline host's loaded sessions once each, even a session loaded from both Live and Needs you", () => {
	const studio = [row("studio:a", "studio"), row("studio:b", "studio"), row("studio:c", "studio")];
	expect(
		notices({
			...none,
			sources: [source("laptop", "Laptop", true), source("studio", "Studio Mac", false)],
			// Live's rows, then Needs you's: studio:a is in both.
			loadedRows: [row("laptop:x", "laptop"), ...studio, studio[0]],
		}),
	).toEqual([
		{
			key: "host:studio",
			kind: "host",
			text: "Studio Mac is offline · 3 sessions",
			action: "Details",
			sourceId: "studio",
		},
	]);
});

it("says 1 session for one, and drops the count when nothing loaded names the host", () => {
	expect(
		notices({
			...none,
			sources: [source("studio", "Studio Mac", false), source("rack", "Rack", false)],
			loadedRows: [row("studio:a", "studio")],
		}).map((notice) => notice.text),
	).toEqual(["Studio Mac is offline · 1 session", "Rack is offline"]);
});

it("names each broken plugin", () => {
	expect(notices({ ...none, plugins: [plugin("superpowers", false), plugin("elements-of-style", true)] })).toEqual([
		{
			key: "plugin:elements-of-style@evener",
			kind: "plugin",
			text: "elements-of-style is broken",
			action: "Plugins",
			pluginId: "elements-of-style",
			marketplace: "evener",
		},
	]);
});

it("keeps two broken plugins of one name from different marketplaces apart", () => {
	const found = notices({
		...none,
		plugins: [plugin("superpowers", true), { ...plugin("superpowers", true), marketplace: "community" }],
	});
	expect(found.map((notice) => notice.key)).toEqual(["plugin:superpowers@evener", "plugin:superpowers@community"]);
	// Their names alone would read the same, so each names its marketplace.
	expect(found.map((notice) => notice.text)).toEqual([
		"superpowers from evener is broken",
		"superpowers from community is broken",
	]);
	// Each opens the Hub at its own plugin.
	expect(found.map((notice) => (notice.kind === "plugin" ? notice.marketplace : null))).toEqual([
		"evener",
		"community",
	]);
});

it("names no marketplace when the plugin sharing a broken one's name is healthy", () => {
	const found = notices({
		...none,
		plugins: [plugin("superpowers", true), { ...plugin("superpowers", false), marketplace: "community" }],
	});
	expect(found.map((notice) => notice.text)).toEqual(["superpowers is broken"]);
});

it("lists sign-ins, then hosts, then plugins", () => {
	expect(
		notices({
			auth: [provider("openai", { needsLogin: true })],
			sources: [source("studio", "Studio Mac", false)],
			plugins: [plugin("superpowers", true)],
			loadedRows: [],
		}).map((notice) => notice.kind),
	).toEqual(["signIn", "host", "plugin"]);
});
