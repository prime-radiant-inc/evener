// The repeat-prebuild qualification compares the generated Xcode project
// through its complete parsed content, not its bytes: Expo's writer may
// reformat an unchanged empty PBX dictionary. These cases run the real
// repository canonicalizer against the real installed @bacons/xcode parser.
import { execFile } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { afterAll, expect, it } from "vitest";

const run = promisify(execFile);

const script = join(__dirname, "../../scripts/native/ios/qualification/project-canonicalize.mjs");
const nativeRoot = join(__dirname, "..");

// A minimal generated project: every shape the byte-comparison must tolerate
// is present — a multiline empty dictionary and object keys the writer may
// reorder. Opaque hex identifiers stand in for Xcode UUIDs.
const baseProject = `// !$*UTF8*$!
{
	archiveVersion = 1;
	classes = {
	};
	objectVersion = 70;
	objects = {

		AA0000000000000000000001 /* Project object */ = {
			isa = PBXProject;
			attributes = {
				TargetAttributes = {
				};
			};
			buildConfigurationList = BB0000000000000000000002;
			compatibilityVersion = "Xcode 16.0";
			mainGroup = CC0000000000000000000003;
			targets = (
			);
		};

		BB0000000000000000000002 = {
			isa = XCConfigurationList;
			buildConfigurations = (
			);
			defaultConfigurationIsVisible = 0;
		};

		CC0000000000000000000003 = {
			isa = PBXGroup;
			children = (
				DD0000000000000000000004,
			);
			sourceTree = "<group>";
		};

		DD0000000000000000000004 = {
			isa = PBXFileSystemSynchronizedRootGroup;
			exceptions = (
			);
			explicitFileTypes = {
			};
			explicitFolders = (
			);
			path = share;
			sourceTree = "<group>";
		};
	};
	rootObject = AA0000000000000000000001 /* Project object */;
}
`;

const workDir = mkdtempSync(join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "project-canonicalize-"));
afterAll(() => rmSync(workDir, { recursive: true, force: true }));

async function canonicalize(name: string, source: string): Promise<string> {
	const projectPath = join(workDir, `${name}.pbxproj`);
	const outputPath = join(workDir, `${name}.json`);
	writeFileSync(projectPath, source);
	await run(process.execPath, [script, nativeRoot, projectPath, outputPath]);
	return readFileSync(outputPath, "utf8");
}

it("treats writer reformatting and key reordering of unchanged content as identical", async () => {
	const base = await canonicalize("base", baseProject);
	// The same project as Expo's writer re-emits it: the empty dictionary is
	// collapsed to one line and the synchronized group's keys are reordered.
	const reformatted = await canonicalize(
		"reformatted",
		baseProject
			.replace("explicitFileTypes = {\n\t\t\t};", "explicitFileTypes = {};")
			.replace(
				`\t\t\tisa = PBXFileSystemSynchronizedRootGroup;\n\t\t\texceptions = (\n\t\t\t);\n\t\t\texplicitFileTypes = {};\n\t\t\texplicitFolders = (\n\t\t\t);\n\t\t\tpath = share;\n\t\t\tsourceTree = "<group>";`,
				`\t\t\texceptions = (\n\t\t\t);\n\t\t\texplicitFolders = (\n\t\t\t);\n\t\t\texplicitFileTypes = {};\n\t\t\tpath = share;\n\t\t\tisa = PBXFileSystemSynchronizedRootGroup;\n\t\t\tsourceTree = "<group>";`,
			),
	);
	expect(reformatted).toBe(base);
});

it("reports changed project content as different", async () => {
	const base = await canonicalize("unchanged", baseProject);
	const changed = await canonicalize("changed", baseProject.replace("path = share;", "path = widget;"));
	expect(changed).not.toBe(base);
});
