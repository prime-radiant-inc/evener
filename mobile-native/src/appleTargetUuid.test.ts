import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it, onTestFinished } from "vitest";
import { requireFrom } from "./nativeTooling.testkit";

const appleTargets = requireFrom(["@bacons/apple-targets"]);
const bacons = requireFrom(["@bacons/apple-targets", "@bacons/xcode"]);
const uuid = bacons("uuid");

// RFC name-based vectors for "x" in the DNS namespace, computed independently
// with Node crypto's MD5/SHA1 digests and the RFC version/variant bits.
for (const [method, bytes] of [
	["v3", [236, 121, 78, 254, 163, 132, 59, 17, 160, 182, 236, 137, 149, 188, 106, 204]],
	["v5", [5, 177, 106, 1, 70, 198, 86, 221, 189, 110, 198, 223, 180, 161, 66, 122]],
] as const) {
	describe(method, () => {
		for (const [name, size, offset] of [
			["short buffer", 8, 4],
			["negative offset", 20, -1],
			["overflowing offset", 20, 5],
		] as const) {
			it(`rejects ${name} without partial writes`, () => {
				const out = new Uint8Array(size).fill(0xaa);
				expect.soft(() => uuid[method]("x", uuid[method].DNS, out, offset)).toThrow(RangeError);
				expect([...out]).toEqual(Array(size).fill(0xaa));
			});
		}

		it("writes only the requested UUID output region", () => {
			const out = new Uint8Array(24).fill(0xaa);
			expect(uuid[method]("x", uuid[method].DNS, out, 4)).toBe(out);
			expect([...out]).toEqual([0xaa, 0xaa, 0xaa, 0xaa, ...bytes, 0xaa, 0xaa, 0xaa, 0xaa]);
		});
	});
}

it("loads bacons' actual UUID dependency and creates distinct valid v4 identifiers", () => {
	const ids = Array.from({ length: 16 }, () => uuid.v4());
	expect(new Set(ids).size).toBe(ids.length);
	for (const id of ids) expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
});

function templateProjectPath(): string {
	const dir = mkdtempSync(join(tmpdir(), "evener-pbx-uuid-"));
	onTestFinished(() => rmSync(dir, { recursive: true, force: true }));
	const file = join(dir, "project.pbxproj");
	const native = requireFrom([]);
	const template = join(native.resolve("expo/package.json"), "..", "template.tgz");
	writeFileSync(file, execFileSync("tar", ["-xOf", template, "package/ios/HelloWorld.xcodeproj/project.pbxproj"]));
	return file;
}

function expectReferencesResolve(value: unknown, ids: Set<string>): void {
	if (typeof value === "string" && /^[A-Z0-9]{24}$/.test(value)) {
		expect(ids.has(value)).toBe(true);
	} else if (value && typeof value === "object") {
		for (const child of Object.values(value)) expectReferencesResolve(child, ids);
	}
}

for (const [name, chain] of [
	["SDK config plugins", ["expo", "@expo/config-plugins"]],
	["Apple-target config plugins", ["@bacons/apple-targets", "@expo/config-plugins"]],
] as const) {
	it(`${name} retain reachable source and extension objects through a PBX round trip`, () => {
		const owner = requireFrom(chain);
		const xcode = owner("xcode");
		const file = templateProjectPath();
		const project = xcode.project(file).parseSync();
		const target = project.addTarget(
			"QualificationShare",
			"app_extension",
			"QualificationShare",
			"com.primeradiant.evener.native.QualificationShare",
		);
		project.addBuildPhase([], "PBXSourcesBuildPhase", "Sources", target.uuid);
		const source = project.addSourceFile(
			"Qualification.swift",
			{ target: target.uuid },
			project.getFirstProject().firstProject.mainGroup,
		);
		writeFileSync(file, project.writeSync());
		const restored = xcode.project(file).parseSync();
		const ids: string[] = restored.allUuids();
		expect(new Set(ids).size).toBe(ids.length);
		for (const id of ids) expect(id).toMatch(/^[A-F0-9]{24}$/);
		expect(ids).toEqual(expect.arrayContaining([target.uuid, source.uuid, source.fileRef]));
		expect(restored.hash.project.objects.PBXNativeTarget[target.uuid].productType).toBe(
			'"com.apple.product-type.app-extension"',
		);
		expect(restored.hash.project.objects.PBXBuildFile[source.uuid].fileRef).toBe(source.fileRef);
		expect(restored.getFirstProject().firstProject.targets).toEqual(
			expect.arrayContaining([expect.objectContaining({ value: target.uuid })]),
		);
		expectReferencesResolve(restored.hash.project, new Set(ids));
	});
}

it("bacons preserves collision-free path-derived file references through its public project API", () => {
	const { XcodeProject } = appleTargets("@bacons/xcode");
	const json = appleTargets("@bacons/xcode/json");
	const file = templateProjectPath();
	const project = XcodeProject.open(file);
	const group = project.rootObject.props.mainGroup;
	const source = group.createFile({ path: "Qualification.swift", sourceTree: "<group>" });
	const duplicate = group.createFile({ path: "Qualification.swift", sourceTree: "<group>" });
	const other = group.createFile({ path: "Other.swift", sourceTree: "<group>" });
	const phase = project.rootObject.getMainAppTarget().getSourcesBuildPhase();
	phase.createFile({ fileRef: source });
	expect(new Set([source.uuid, duplicate.uuid, other.uuid]).size).toBe(3);
	const ids: string[] = [...project.keys()];
	for (const id of ids) expect(id).toMatch(/^(?:[A-F0-9]{24}|XX[A-F0-9]{20}XX)$/);
	expectReferencesResolve(project.toJSON(), new Set(ids));
	writeFileSync(file, json.build(project.toJSON()));
	const restored = XcodeProject.open(file);
	expect([...restored.keys()].sort()).toEqual(ids.sort());
	expect(restored.getObject(source.uuid).props.path).toBe("Qualification.swift");
	expect(restored.rootObject.getMainAppTarget().getSourcesBuildPhase().includesFile(source.uuid)).toBe(true);
	expect(restored.rootObject.props.mainGroup.props.children.map((child: { uuid: string }) => child.uuid)).toEqual(
		expect.arrayContaining([source.uuid, duplicate.uuid, other.uuid]),
	);
	expect(json.parse(readFileSync(file, "utf8")).rootObject).toBe(project.rootObject.uuid);
	expectReferencesResolve(restored.toJSON(), new Set(ids));
});
