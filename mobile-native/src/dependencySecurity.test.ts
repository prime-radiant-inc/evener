import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

interface PackageManifest {
	devDependencies?: Record<string, string>;
	version?: string;
}

interface PackageLock {
	packages: Record<string, PackageManifest>;
}

function readJson<T>(relativePath: string): T {
	return JSON.parse(readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), "utf8")) as T;
}

describe("native dependency security resolutions", () => {
	it("locks and installs the approved remediated versions", () => {
		const manifest = readJson<PackageManifest>("../package.json");
		const lock = readJson<PackageLock>("../package-lock.json");
		const expected = {
			"@react-navigation/core": "7.23.0",
			"brace-expansion": "5.0.12",
			dompurify: "3.4.16",
		};

		expect(manifest.devDependencies?.["@react-navigation/core"]).toBe("^7.23.0");
		expect(manifest.devDependencies?.dompurify).toBe(expected.dompurify);
		for (const [name, version] of Object.entries(expected)) {
			expect(lock.packages[`node_modules/${name}`]?.version, `${name} lock version`).toBe(version);
			expect(
				readJson<PackageManifest>(`../node_modules/${name}/package.json`).version,
				`${name} installed version`,
			).toBe(version);
		}
	});

	it("removes the vulnerable navigation query decoder chain", () => {
		const lock = readJson<PackageLock>("../package-lock.json");
		for (const name of ["query-string", "decode-uri-component"]) {
			expect(lock.packages).not.toHaveProperty(`node_modules/${name}`);
			expect(existsSync(fileURLToPath(new URL(`../node_modules/${name}`, import.meta.url)))).toBe(false);
		}
	});
});
