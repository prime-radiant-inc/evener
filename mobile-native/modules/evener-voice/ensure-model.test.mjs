import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
	copyFileSync,
	mkdirSync,
	mkdtempSync,
	readFileSync,
	readdirSync,
	rmSync,
	statSync,
	writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, expect, it } from "vitest";

let root;
let manifest;
const output = () => join(root, "resources/wake-models.json");
const priorOutput = '{"id":"prior-build"}\n';
const MODEL_NAMES = ["melspectrogram", "embedding", "classifier"];

beforeEach(() => {
	root = mkdtempSync(join(process.env.EVENER_SCRATCH_DIR || tmpdir(), "wake-installer-"));
	copyInstaller(root);
	manifest = {
		id: "installer-test",
		phrase: "Hey Evener.",
		settings: { threshold: 0.99, releaseThreshold: 0.5, hopSamples: 1280, holdoffMs: 2500, embeddingFrames: 16 },
	};
	for (const name of MODEL_NAMES) {
		const bytes = Buffer.from(`installer checksum fixture: ${name}`);
		writeFileSync(join(root, `${name}.onnx`), bytes);
		manifest[name] = {
			file: `${name}.onnx`,
			source: `./${name}.onnx`,
			sha256: createHash("sha256").update(bytes).digest("hex"),
		};
	}
});

afterEach(() => rmSync(root, { recursive: true, force: true }));

function copyInstaller(dir) {
	mkdirSync(join(dir, "resources"), { recursive: true });
	copyFileSync(new URL("./ensure-model.mjs", import.meta.url), join(dir, "ensure-model.mjs"));
	// The npm test script installs this checksum-pinned asset before the suite runs.
	copyFileSync(new URL("./resources/silero_vad.onnx", import.meta.url), join(dir, "resources/silero_vad.onnx"));
}

function runInstaller(dir, args = []) {
	const result = spawnSync(process.execPath, [join(dir, "ensure-model.mjs"), ...args], {
		encoding: "utf8",
		timeout: 10000,
	});
	if (result.error) throw result.error;
	return result;
}

function writeManifest(dir = root) {
	writeFileSync(join(dir, "wake-models.custom.json"), JSON.stringify(manifest));
}

function install() {
	writeManifest();
	return runInstaller(root);
}

// Mirrors the real layout: <app>/modules/evener-voice/ensure-model.mjs reads <app>/package.json.
function appFixture({ exclude, defaultManifest = false } = {}) {
	const app = join(root, "app");
	const moduleDir = join(app, "modules/evener-voice");
	copyInstaller(moduleDir);
	if (exclude !== undefined)
		writeFileSync(join(app, "package.json"), JSON.stringify({ expo: { autolinking: { ios: { exclude } } } }));
	if (defaultManifest) {
		for (const name of MODEL_NAMES)
			copyFileSync(join(root, `${name}.onnx`), join(moduleDir, `${name}.onnx`));
		writeManifest(moduleDir);
	}
	return { moduleDir, run: (...args) => runInstaller(moduleDir, args) };
}

it.each([
	["id", ""],
	["id", "   "],
	["id", null],
	["phrase", ""],
	["phrase", 42],
	["threshold", 0],
	["threshold", 1.01],
	["threshold", "0.99"],
	["threshold", null],
	["releaseThreshold", -0.1],
	["releaseThreshold", 0.99],
	["releaseThreshold", 0.989999999], // Equal to threshold after Swift Float decoding.
	["releaseThreshold", "0.5"],
	["hopSamples", 0],
	["hopSamples", 1281],
	["hopSamples", 16640],
	["hopSamples", "1280"],
	["holdoffMs", -1],
	["holdoffMs", 1.5],
	["holdoffMs", "2500"],
	["embeddingFrames", 0],
	["embeddingFrames", 121],
	["embeddingFrames", 1.5],
])("rejects invalid %s=%j without replacing the packaged manifest", (field, value) => {
	if (field === "id" || field === "phrase") manifest[field] = value;
	else manifest.settings[field] = value;
	writeFileSync(output(), priorOutput);
	const result = install();
	expect(result.status, result.stderr).not.toBe(0);
	expect(result.stderr).toContain("Invalid wake manifest");
	expect(readFileSync(output(), "utf8")).toBe(priorOutput);
	expect(readdirSync(join(root, "resources"))).toEqual(["silero_vad.onnx", "wake-models.json"]);
});

it("rejects missing settings before installing wake assets", () => {
	delete manifest.settings;
	const result = install();
	expect(result.status, result.stderr).not.toBe(0);
	expect(result.stderr).toContain("Invalid wake manifest");
	expect(readdirSync(join(root, "resources"))).toEqual(["silero_vad.onnx"]);
});

it.each([1, 120])("accepts boundary settings with %i embedding frames", (embeddingFrames) => {
	manifest.settings = { threshold: 1, releaseThreshold: 0, hopSamples: 15360, holdoffMs: 0, embeddingFrames };
	const result = install();
	expect(result.status, result.stderr).toBe(0);
	const packaged = JSON.parse(readFileSync(output(), "utf8"));
	expect(packaged.settings).toEqual(manifest.settings);
	expect(packaged.classifier).toEqual({ file: "classifier.onnx", sha256: manifest.classifier.sha256 });
	expect(packaged).not.toHaveProperty("classifier.source");
});

it("replaces a truncated output atomically and leaves no temporary file", () => {
	writeFileSync(output(), '{"id":');
	const oldInode = statSync(output()).ino;
	const result = install();
	expect(result.status, result.stderr).toBe(0);
	expect(JSON.parse(readFileSync(output(), "utf8")).id).toBe(manifest.id);
	expect(statSync(output()).ino).not.toBe(oldInode);
	expect(readdirSync(join(root, "resources")).sort()).toEqual([
		"classifier.onnx",
		"embedding.onnx",
		"melspectrogram.onnx",
		"silero_vad.onnx",
		"wake-models.json",
	]);
	const repeated = install();
	expect(repeated.status, repeated.stderr).toBe(0);
});

it("still refuses a corrupt cached model without replacing the build input", () => {
	writeFileSync(output(), priorOutput);
	writeFileSync(join(root, "resources/classifier.onnx"), "corrupt");
	const result = install();
	expect(result.status, result.stderr).not.toBe(0);
	expect(result.stderr).toContain("Existing classifier.onnx checksum mismatch; refusing to replace it");
	expect(readFileSync(output(), "utf8")).toBe(priorOutput);
});

it("skips the postinstall install while the app excludes evener-voice", () => {
	// A nonexistent source makes any install attempt fail, so exit 0 proves the manifest was not read.
	for (const name of ["melspectrogram", "embedding", "classifier"]) manifest[name].source = `./missing-${name}.onnx`;
	const app = appFixture({ exclude: ["evener-voice"], defaultManifest: true });
	const result = app.run("--skip-if-excluded");
	expect(result.status, result.stderr).toBe(0);
	expect(result.stdout).toContain("evener-voice is excluded from the iOS build");
	expect(readdirSync(join(app.moduleDir, "resources"))).toEqual(["silero_vad.onnx"]);
});

it.each([
	["without an exclusion entry", { exclude: [] }],
	["without an app package", {}],
])("still installs with --skip-if-excluded %s", (_label, options) => {
	writeManifest();
	const app = appFixture(options);
	const result = app.run("--skip-if-excluded", "--wake-manifest", join(root, "wake-models.custom.json"));
	expect(result.status, result.stderr).toBe(0);
	expect(JSON.parse(readFileSync(join(app.moduleDir, "resources/wake-models.json"), "utf8")).id).toBe(manifest.id);
});

it("installs the default manifest while the app excludes evener-voice when the flag is absent", () => {
	const app = appFixture({ exclude: ["evener-voice"], defaultManifest: true });
	const result = app.run();
	expect(result.status, result.stderr).toBe(0);
	expect(JSON.parse(readFileSync(join(app.moduleDir, "resources/wake-models.json"), "utf8")).id).toBe(manifest.id);
});
