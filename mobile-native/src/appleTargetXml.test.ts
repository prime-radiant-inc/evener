import { describe, expect, it } from "vitest";
import { requireFrom } from "./nativeTooling.testkit";

const appleTargets = requireFrom(["@bacons/apple-targets"]);
const xcode = requireFrom(["@bacons/apple-targets", "@bacons/xcode"]);
const plistOwner = requireFrom(["@bacons/apple-targets", "@bacons/xcode", "@expo/plist"]);

for (const [name, owner] of [
	["bacons workspace and scheme", xcode],
	["legacy plist", plistOwner],
] as const) {
	describe(name, () => {
		const { DOMParser, XMLSerializer } = owner("@xmldom/xmldom");

		it("rejects CDATA injection without changing the document", () => {
			const doc = new DOMParser().parseFromString("<root/>");
			expect(() => doc.createCDATASection("]]><injected/>")).toThrow(expect.objectContaining({ code: 5 }));
			expect(doc.getElementsByTagName("injected").length).toBe(0);
			expect(new XMLSerializer().serializeToString(doc)).toBe("<root/>");
		});

		it("preserves text and a single root through malformed-tag recovery", () => {
			// GHSA-93r5-fhx6-vmg9's finite recovery fixture, without a timing oracle.
			const diagnostics: string[] = [];
			const text = "a<".repeat(32);
			const doc = new DOMParser({ errorHandler: (error: string) => diagnostics.push(error) }).parseFromString(
				`<r>${text}</r>`,
				"text/xml",
			);
			expect(diagnostics.length).toBeGreaterThan(0);
			expect(doc.documentElement.nodeName).toBe("r");
			expect(doc.documentElement.textContent).toBe(text);
			expect(doc.documentElement.childNodes.length).toBe(1);
			const restored = new DOMParser().parseFromString(new XMLSerializer().serializeToString(doc), "text/xml");
			expect(restored.documentElement.textContent).toBe(text);
			expect(restored.getElementsByTagName("r").length).toBe(1);
		});
	});
}

it("round trips legacy plist values through its actual MIME-less parser", () => {
	const plist = xcode("@expo/plist").default;
	const values = {
		"com.apple.security.application-groups": ["group.com.primeradiant.evener.native"],
		enabled: true,
		disabled: false,
		count: 3,
		text: "A & B, 議事録 Δ",
		date: new Date("2026-11-04T00:00:00.000Z"),
		data: Buffer.from([0, 1, 127, 128, 255]),
	};
	expect(plist.parse(plist.build(values))).toEqual(values);
	const text = "a<".repeat(32);
	expect(plist.parse(`<plist version="1.0"><dict><key>value</key><string>${text}</string></dict></plist>`)).toEqual({
		value: text,
	});
});

it("round trips bacons workspace references and groups without injecting nodes", () => {
	const workspace = appleTargets("@bacons/xcode/workspace");
	const values = {
		version: "1.0",
		fileRefs: [{ location: "group:A & B.xcodeproj" }],
		groups: [
			{
				name: "議事録 & Δ",
				location: "group:Modules",
				fileRefs: [{ location: "group:Share.xcodeproj" }],
			},
		],
	};
	const xml = workspace.build(values);
	expect(workspace.parse(xml)).toEqual(values);
	const { DOMParser } = xcode("@xmldom/xmldom");
	const doc = new DOMParser().parseFromString(xml, "text/xml");
	expect(doc.documentElement.nodeName).toBe("Workspace");
	expect(doc.getElementsByTagName("FileRef").length).toBe(2);
	expect(doc.getElementsByTagName("Group").item(0).getAttribute("name")).toBe("議事録 & Δ");
});

it("round trips bacons scheme build targets and escaped launch values", () => {
	const scheme = appleTargets("@bacons/xcode/scheme");
	const reference = {
		buildableIdentifier: "primary",
		blueprintIdentifier: "0123456789ABCDEF01234567",
		buildableName: "A & B.app",
		blueprintName: "議事録 Δ",
		referencedContainer: "container:A & B.xcodeproj",
	};
	const values = {
		version: "1.7",
		buildAction: {
			parallelizeBuildables: true,
			buildImplicitDependencies: false,
			entries: [{ buildForRunning: true, buildForTesting: false, buildableReference: reference }],
		},
		launchAction: {
			buildConfiguration: "Release",
			environmentVariables: [{ key: "FIXTURE", value: "A & B Δ", isEnabled: true }],
		},
	};
	expect(scheme.parse(scheme.build(values))).toEqual(values);
});
