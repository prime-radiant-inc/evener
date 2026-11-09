import * as fs from "node:fs";
import { join } from "node:path";
import { requireFrom } from "./nativeTooling.testkit";

const [mode, encodedChain, cwd, timing] = process.argv.slice(2);
const chain: string[] = JSON.parse(encodedChain ?? "[]");
const owner = requireFrom(chain);
const result = {
	completed: false,
	readsAfterAbort: 0,
	operationsAfterAbort: [] as { operation: string; path: string }[],
	directoryReads: 0,
	aborted: false,
	abortObserved: false,
	paused: false,
	files: [] as string[],
	compiledAlternatives: 0,
	apple: undefined as unknown,
};

// A rejected walk can still have filesystem callbacks in flight. Report only
// after Node has drained those callbacks, including work queued after abort.
process.once("beforeExit", () => console.log(JSON.stringify(result)));

if (mode === "apple-targets") {
	const apple = requireFrom(["@bacons/apple-targets"]);
	const config = apple("./config-plugin")({
		name: "HelloWorld",
		slug: "hello-world",
		version: "1.0.0",
		ios: {
			bundleIdentifier: "com.primeradiant.evener.native",
			appleTeamId: "EVENERCI01",
		},
		_internal: { projectRoot: cwd },
	});
	const compiled = await apple("expo/config-plugins").compileModsAsync(config, {
		projectRoot: cwd,
		platforms: ["ios"],
	});
	const project = apple("@bacons/xcode").XcodeProject.open(join(cwd ?? "", "ios/HelloWorld.xcodeproj/project.pbxproj"));
	result.apple = {
		extensions: compiled.extra.eas.build.experimental.ios.appExtensions,
		project: project.toJSON(),
	};
	result.completed = true;
} else if (mode === "minimatch-growth") {
	const { Minimatch } = owner("minimatch");
	const matcher = new Minimatch(Array(30).fill("**/../a/b").join("/"), {
		optimizationLevel: 2,
		braceExpandMax: 64,
	});
	result.compiledAlternatives = matcher.set.length;
	result.completed = true;
} else {
	const glob = owner("glob");
	const controller = new AbortController();
	const reason = new Error("fixture cancellation");
	function observe<T extends (...args: never[]) => unknown>(operation: string, read: T, directory = false): T {
		return ((...args: Parameters<T>) => {
			if (controller.signal.aborted) {
				result.readsAfterAbort++;
				result.operationsAfterAbort.push({ operation, path: String(args[0]) });
			}
			if (directory) result.directoryReads++;
			const value = read(...args);
			if (directory && timing === "during" && result.directoryReads === 1) controller.abort(reason);
			return value;
		}) as T;
	}
	const observedFs = {
		lstatSync: observe("lstatSync", fs.lstatSync),
		readdirSync: observe("readdirSync", fs.readdirSync, true),
		readlinkSync: observe("readlinkSync", fs.readlinkSync),
		realpathSync: observe("realpathSync", fs.realpathSync),
		readdir: observe("readdir", fs.readdir, true),
		promises: {
			lstat: observe("lstat", fs.promises.lstat),
			readdir: observe("readdir", fs.promises.readdir, true),
			readlink: observe("readlink", fs.promises.readlink),
			realpath: observe("realpath", fs.promises.realpath),
		},
	};
	if (timing === "before") controller.abort(reason);
	const options = { cwd, nodir: true, dot: true, signal: controller.signal, fs: observedFs };
	try {
		if (mode === "cancel-promise") result.files = await glob.glob("**/*.{ts,js}", options);
		else if (mode === "cancel-sync") result.files = glob.globSync("**/*.{ts,js}", options);
		else if (mode === "cancel-stream" || mode === "resume") {
			const stream = glob.globStream("**/*.{ts,js}", options);
			await new Promise<void>((resolve, reject) => {
				stream.once("end", resolve);
				stream.once("error", (error: unknown) => (error === reason ? resolve() : reject(error)));
				stream.on("data", (file: string) => {
					result.files.push(file);
					if ((timing === "paused" || mode === "resume") && !result.paused) {
						stream.pause();
						result.paused = true;
						queueMicrotask(() => {
							if (timing === "paused") controller.abort(reason);
							stream.resume();
						});
					}
				});
			});
			result.abortObserved = stream.aborted;
		} else throw new TypeError("unknown probe mode");
		result.completed = true;
	} catch (error) {
		if (error !== reason) throw error;
		result.abortObserved = true;
		result.completed = true;
	}
	result.aborted = controller.signal.aborted;
}
