import { expect, it } from "vitest";
import { hubPagePath, hubPageSource } from "./hubWebPolicy";

const origin = "https://hub.example:9180";

it("bootstraps the paired hub's session page with its HTTP cookie", () => {
	const source = hubPageSource(origin, "/s/host%3Asession", "hello");
	expect(source).toEqual({
		uri: origin + "/s/host%3Asession",
		headers: { Cookie: "evener_hub_auth_pDDYRoCqvQs=hello" },
	});
	expect(hubPageSource(origin, "//elsewhere.test/", "hello")?.uri).toBe(origin + "/");
	expect(hubPageSource(origin, "/", "desk token, one")?.headers?.Cookie).toBe(
		'evener_hub_auth_s2yYReIvKTM="desk token, one"',
	);
	for (const value of ["hello; other=value", 'hello"world', "hello\\world", "hello\r\nworld"]) {
		expect(hubPageSource(origin, "/", value)).toBeNull();
	}
});

it("trusts the paired hub UI, including SPA session changes", () => {
	expect(hubPagePath(origin, origin + "/s/host%3Asession")).toBe("/s/host%3Asession");
	for (const path of ["/new?dir=%2Fwork&prompt=keep%20this#setup", "/settings/hosts?host=studio"]) {
		expect(hubPagePath(origin, origin + path)).toBe(path);
		expect(hubPageSource(origin, path, "hello")?.uri).toBe(origin + path);
	}
	for (const url of [
		"https://hub.example:9181/",
		"http://hub.example:9180/",
		origin + "/doc/file?path=index.html",
		origin + "/auth/capability",
		origin + "/?token=capability",
		"https://another@hub.example:9180/",
	]) {
		expect(hubPagePath(origin, url)).toBeNull();
	}
});
