// The demo hub's answers for New session and the Hub (phase 5's Task 26):
// every one is typed as its method's result in demoSetup.ts, so npm run check
// holds them to the wire; these cases pin what the screenshots rely on.
import { describe, expect, it } from "vitest";
import { createDemoFleet } from "./demoFleet.js";
import { createDemoSetup } from "./demoSetup.js";

const setup = (offlineHost = false) => createDemoSetup(createDemoFleet({ offlineHost }), { offlineHost });

describe("hosts and updates", () => {
	it("lists paradise-park from hub.toml, attached, on an older version", () => {
		const [host] = setup().answer("evener/host/list", {}).hosts;
		expect(host).toMatchObject({
			name: "paradise-park",
			origin: "hub.toml",
			attached: true,
			midAttach: false,
			os: "darwin",
			arch: "arm64",
			hubVersion: "0.9.409",
			roots: ["/Users/jesse/git"],
		});
	});

	it("marks paradise-park offline with its last error under the offline flag (frame 24)", () => {
		const [host] = setup(true).answer("evener/host/list", {}).hosts;
		expect(host).toMatchObject({
			attached: false,
			midAttach: false,
			hubVersion: "0.9.409",
			lastAttachError: "ssh: connect to host paradise-park port 22: Operation timed out",
		});
	});

	it("attaches an offline host on Connect", () => {
		const demo = setup(true);
		expect(demo.answer("evener/host/attach", { host: "paradise-park" })).toMatchObject({ attached: true });
		expect(demo.answer("evener/host/list", {}).hosts[0]).toMatchObject({ attached: true });
		expect(demo.answer("evener/host/list", {}).hosts[0]?.lastAttachError).toBeUndefined();
	});

	it("says the hub runs 0.9.412 and is up to date", () => {
		expect(setup().answer("evener/update/check", {})).toMatchObject({
			currentVersion: "0.9.412",
			channel: "release",
			applicable: true,
			updateAvailable: false,
		});
	});
});

describe("providers", () => {
	it("lists the prototype's eleven providers with lunaroute the default", () => {
		const { instances } = setup().answer("evener/instance/list", {});
		expect(instances).toHaveLength(11);
		expect(instances.filter((instance) => instance.isDefault).map((instance) => instance.name)).toEqual(["lunaroute"]);
		expect(instances.find((instance) => instance.name === "ollama")).toMatchObject({ credentialRequired: false });
	});

	it("turns a model off and back on, and lists it either way", () => {
		const demo = setup();
		const lunaroute = (list: { instances: { name: string; models?: { id: string; disabled?: boolean }[] }[] }) =>
			list.instances.find((instance) => instance.name === "lunaroute")?.models;
		const off = demo.answer("evener/instance/setModelDisabled", { name: "lunaroute", model: "glm-5.3", disabled: true });
		expect(lunaroute(off)).toContainEqual({ id: "glm-5.3", disabled: true });
		expect(lunaroute(demo.answer("evener/instance/list", {}))).toContainEqual({ id: "glm-5.3", disabled: true });
		const on = demo.answer("evener/instance/setModelDisabled", { name: "lunaroute", model: "glm-5.3", disabled: false });
		expect(lunaroute(on)).toContainEqual({ id: "glm-5.3" });
	});

	it("finds lunaroute's new model on a check, and nothing new elsewhere", () => {
		const demo = setup();
		const models = (list: { instances: { name: string; models?: { id: string }[] }[] }, name: string) =>
			list.instances.find((instance) => instance.name === name)?.models?.map((model) => model.id);
		expect(models(demo.answer("evener/instance/list", {}), "lunaroute")).not.toContain("glm-5.4");
		const checked = demo.answer("evener/instance/refreshModels", { name: "lunaroute" });
		expect(models(checked, "lunaroute")).toContain("glm-5.4");
		expect(models(demo.answer("evener/instance/refreshModels", { name: "meta" }), "meta")).toEqual(["muse-spark-1.3"]);
	});

	it("refuses a model change or a check for a provider it doesn't have, as the hub does", () => {
		const demo = setup();
		expect(() =>
			demo.answer("evener/instance/setModelDisabled", { name: "nowhere", model: "k3", disabled: true }),
		).toThrow('instance "nowhere" not found');
		expect(() => demo.answer("evener/instance/refreshModels", { name: "nowhere" })).toThrow(
			'instance "nowhere" not found',
		);
	});

	it("describes each provider's sign-in the way the hub does (app_auth.go's authModesFor)", () => {
		const byName = new Map(
			setup()
				.answer("evener/instance/list", {})
				.instances.map((entry) => [entry.name, entry]),
		);
		expect(byName.get("lunaroute")).toMatchObject({ auth: "bearer", authModes: ["apiKey"], activeSource: "store" });
		expect(byName.get("codex-jesse-fsck.com")).toMatchObject({
			auth: "oauth-openai-codex",
			authModes: ["oauth"],
			activeSource: "oauth",
			hasStoredOAuth: true,
		});
		expect(byName.get("vertex")).toMatchObject({
			auth: "gcp-adc",
			authModes: ["adc", "credentialJson"],
			activeSource: "adc",
		});
		expect(byName.get("ollama")).toMatchObject({ auth: "none", authModes: ["none"], activeSource: "none" });
	});

	it("reports every account sign-in, with the expired one needing a sign-in", () => {
		const statuses = setup().answer("evener/auth/list", {}).providers;
		expect(statuses.map((status) => status.provider).sort()).toEqual(["codex-jesse-at-pr", "codex-jesse-fsck.com"]);
		expect(statuses.find((status) => status.provider === "codex-jesse-fsck.com")?.needsLogin).toBe(true);
		expect(statuses.filter((status) => status.needsLogin)).toHaveLength(1);
	});
});

describe("plugins", () => {
	it("lists the prototype's five marketplaces and a catalog to browse", () => {
		const demo = setup();
		expect(demo.answer("evener/marketplace/list", {}).marketplaces.map((entry) => entry.name)).toEqual([
			"superpowers-marketplace",
			"claude-plugins-official",
			"go-skills",
			"prime-radiant-marketplace",
			"simplify-code-dev",
		]);
		expect(
			demo.answer("evener/marketplace/browse", { name: "go-skills" }).plugins.map((plugin) => plugin.name),
		).toEqual(["go", "go-release", "go-spec-reviewer", "fileflow-pathologize", "go-bench", "go-lint-fix", "go-docs"]);
		expect(
			demo
				.answer("evener/marketplace/browse", { name: "claude-plugins-official" })
				.plugins.map((plugin) => plugin.name),
		).toEqual(["frontend-design"]);
		// internal/plugins/errors.go's ErrMarketplaceNotFound.
		expect(() => demo.answer("evener/marketplace/browse", { name: "nowhere" })).toThrow("marketplace not found");
	});

	it("previews the fourteen plugins with their counts, ten on by default", () => {
		const { plugins } = setup().answer("evener/plugin/preview", { cwd: "/home/jesse/git/prime-radiant-inc/evener" });
		expect(plugins).toHaveLength(14);
		expect(plugins.filter((plugin) => plugin.selected)).toHaveLength(10);
		expect(plugins.find((plugin) => plugin.name === "superpowers")).toMatchObject({
			marketplace: "superpowers-marketplace",
			skillCount: 38,
			agentCount: 3,
			commandCount: 6,
			hookCount: 2,
			mcpCount: 0,
		});
	});

	it("honors an explicit selection", () => {
		const { plugins } = setup().answer("evener/plugin/preview", {
			cwd: "/home/jesse/git/prime-radiant-inc/evener",
			launchOverrides: { enabledPlugins: ["go", "superpowers-chrome"] },
		});
		expect(plugins.filter((plugin) => plugin.selected).map((plugin) => plugin.name)).toEqual([
			"superpowers-chrome",
			"go",
		]);
	});

	it("warns that Chrome is missing on the hub's own machine only", () => {
		const demo = setup();
		const local = demo.answer("evener/plugin/preview", { cwd: "/home/jesse/git/prime-radiant-inc/evener" });
		expect(local.diagnostics).toEqual([{ name: "superpowers-chrome", message: "Chrome isn't installed on this host" }]);
		const remote = demo.answer("evener/host/request", {
			host: "paradise-park",
			method: "evener/plugin/preview",
			params: { cwd: "/Users/jesse/git/evener" },
		}) as { diagnostics?: unknown[] };
		expect(remote.diagnostics).toBeUndefined();
	});
});

describe("models", () => {
	it("carries fifteen models and three recent ones", () => {
		const models = setup().answer("model/list", {});
		expect(models.data).toHaveLength(15);
		expect(models.recent?.map((model) => model.model)).toEqual(["deepseek-4.1-flash", "glm-5.3-vision", "gpt-5.6"]);
		expect(models.data.find((model) => model.model === "glm-5.3-vision")).toMatchObject({
			provider: "lunaroute",
			displayName: "GLM 5.3 Vision",
			contextWindow: 200_000,
			inputCostPerMillion: 0.6,
			outputCostPerMillion: 2.2,
			supportsVision: true,
			supportsTools: true,
			reasoningEffortLevels: ["low", "medium", "high", "xhigh", "max"],
		});
	});
});

describe("launching", () => {
	it("offers the prototype's projects as recent on the hub's own machine", () => {
		const recent = setup().answer("evener/projects/recent", {}).data;
		expect(recent?.[0]).toBe("/home/jesse/git/prime-radiant-inc/evener");
		expect(recent).toContain("/home/jesse/git/c-to-wasm");
	});

	it("completes folders, makes new ones, and checks them", () => {
		const demo = setup();
		expect(demo.answer("evener/paths/complete", { prefix: "/home/jesse/git/prime" }).data).toEqual([
			"/home/jesse/git/prime-radiant/",
			"/home/jesse/git/prime-radiant-inc/",
		]);
		expect(demo.answer("evener/path/validate", { path: "/home/jesse/git/scratch", kind: "dir" }).valid).toBe(false);
		expect(demo.answer("evener/dirs/create", { path: "/home/jesse/git/scratch" })).toEqual({
			path: "/home/jesse/git/scratch",
			created: true,
		});
		expect(demo.answer("evener/path/validate", { path: "/home/jesse/git/scratch", kind: "dir" }).valid).toBe(true);
		expect(demo.answer("evener/paths/complete", { prefix: "/home/jesse/git/scr" }).data).toEqual([
			"/home/jesse/git/scratch/",
		]);
	});

	it("completes a top-level prefix from the root", () => {
		expect(setup().answer("evener/paths/complete", { prefix: "/ho" }).data).toEqual(["/home/"]);
	});

	it("lists the home folder for an empty prefix", () => {
		expect(setup().answer("evener/paths/complete", { prefix: "" }).data).toEqual(["/home/jesse/git/"]);
	});

	it("names main as a project's branch, and none outside one", () => {
		const demo = setup();
		expect(demo.answer("evener/git/head", { cwd: "/home/jesse/git/prime-radiant-inc/evener" }).head).toBe("main");
		expect(demo.answer("evener/git/head", { cwd: "/home/jesse/git" }).head).toBe("");
	});

	it("resolves the hub's launch defaults", () => {
		expect(
			setup().answer("evener/launch/resolve", { cwd: "/home/jesse/git/prime-radiant-inc/evener" }).effective,
		).toEqual({
			sandbox: "workspace-write",
			sandboxNet: true,
			contextStrategy: "compact",
			maxSubagentDepth: 2,
			maxRounds: -1,
		});
	});

	it("answers paradise-park's launch reads from its own folders, through the hub", () => {
		const demo = setup();
		const forward = (method: string, params: unknown) =>
			demo.answer("evener/host/request", { host: "paradise-park", method, params });
		expect(forward("evener/projects/recent", {})).toEqual({
			data: ["/Users/jesse/git/evener", "/Users/jesse/git/c-to-wasm"],
		});
		expect(
			forward("evener/path/validate", { path: "/home/jesse/git/prime-radiant-inc/evener", kind: "dir" }),
		).toMatchObject({
			valid: false,
		});
		expect(forward("evener/git/head", { cwd: "/Users/jesse/git/evener" })).toEqual({ head: "main" });
	});

	it("refuses a forwarded method off the hub's list, and a host it doesn't have", () => {
		const demo = setup();
		expect(() =>
			demo.answer("evener/host/request", {
				host: "paradise-park",
				method: "evener/instance/setModelDisabled",
				params: {},
			}),
		).toThrow('method "evener/instance/setModelDisabled" is not a permitted remote admin method');
		expect(() => demo.answer("evener/host/request", { host: "elsewhere", method: "model/list", params: {} })).toThrow(
			'unknown host "elsewhere"',
		);
	});

	it("forwards paradise-park's provider list", () => {
		const forwarded = setup().answer("evener/host/request", {
			host: "paradise-park",
			method: "evener/instance/list",
			params: {},
		}) as { instances: unknown[] };
		expect(forwarded.instances).toHaveLength(11);
	});

	it("reaches paradise-park only while it's attached, as the hub does", () => {
		const demo = setup(true);
		const recent = () =>
			demo.answer("evener/host/request", { host: "paradise-park", method: "evener/projects/recent", params: {} });
		expect(recent).toThrow('host "paradise-park" is not attached');
		demo.answer("evener/host/attach", { host: "paradise-park" });
		expect(recent()).toEqual({ data: ["/Users/jesse/git/evener", "/Users/jesse/git/c-to-wasm"] });
	});

	it("answers paradise-park's harness list, and says so for a forwarded read the demo doesn't serve", () => {
		const demo = setup();
		const forward = (method: string) =>
			demo.answer("evener/host/request", { host: "paradise-park", method, params: {} });
		expect(forward("evener/harnesses/list")).toEqual({ data: [{ id: "evener", label: "Evener" }] });
		expect(() => forward("evener/spawn/slashCatalog")).toThrow("Method not implemented by demonstration server");
	});

	it("leaves methods it doesn't serve to the rest of the demo hub", () => {
		expect(setup().handles("thread/start")).toBe(false);
		expect(setup().handles("model/list")).toBe(true);
	});
});
