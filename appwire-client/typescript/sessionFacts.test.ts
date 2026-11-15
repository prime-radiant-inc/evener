import { describe, expect, it } from "vitest";
import { accessFacts, hostIdOf, loadedPluginNames, sessionHosts } from "./sessionFacts";

describe("the host a session runs on", () => {
  const sources = [
    { id: "local", label: "this host", kind: "local", online: true },
    { id: "paradise-park", label: "Paradise Park", kind: "appwire", online: false },
  ];

  it("reads the host from the ref, and the hub's own for a ref that names none", () => {
    expect(hostIdOf("local:abc")).toBe("local");
    expect(hostIdOf("paradise-park:abc")).toBe("paradise-park");
    expect(hostIdOf("abc")).toBe("local");
  });

  it("names the hub's own machine for the hub, and any other host by its manifest label", () => {
    const host = sessionHosts(sources, "Work hub", true);
    expect(host("local")).toEqual({ label: "Work hub", online: true });
    expect(host("paradise-park")).toEqual({ label: "Paradise Park", online: false });
  });

  it("falls back to the manifest's label for the hub's own machine when the client has no hub name", () => {
    expect(sessionHosts(sources, null, true)("local")).toEqual({ label: sources[0]?.label, online: true });
  });

  it("reads every host as offline while the hub is away, whatever the manifest last said", () => {
    const host = sessionHosts(
      sources.map((source) => ({ ...source, online: true })),
      "Work hub",
      false,
    );
    expect(host("local").online).toBe(false);
    expect(host("paradise-park").online).toBe(false);
  });

  it("names a host the manifest doesn't list by its id, with its connection unknown", () => {
    const host = sessionHosts(undefined, null, true);
    expect(host("local")).toEqual({ label: "local", online: true });
    expect(host("studio")).toEqual({ label: "studio", online: null });
  });
});

describe("the plugins a session loaded", () => {
  it("lists them in a stable order", () => {
    expect(loadedPluginNames({ diagnostics: { plugins: [{ name: "superpowers" }, { name: "go" }] } })).toEqual([
      "go",
      "superpowers",
    ]);
  });

  it("tells an empty inventory from an unknown one", () => {
    expect(loadedPluginNames({ diagnostics: { plugins: [] } })).toEqual([]);
    expect(loadedPluginNames({ diagnostics: undefined })).toBeNull();
    expect(loadedPluginNames({ diagnostics: {} })).toBeNull();
  });
});

describe("the access a session was started with", () => {
  const known = ["off", "workspace-write", "read-only", "restricted"];

  it("names each sandbox mode it knows in words of its own", () => {
    const modes = known.map((sandbox) => accessFacts({ access: { sandbox, network: true } })?.mode);
    expect(new Set(modes).size).toBe(known.length);
    for (const [index, sandbox] of known.entries()) expect(modes[index]).not.toBe(sandbox);
  });

  it("shows a mode it doesn't know by its raw name", () => {
    expect(accessFacts({ access: { sandbox: "container", network: true } })?.mode).toBe("container");
  });

  it("says whether the network is allowed", () => {
    const on = accessFacts({ access: { sandbox: "read-only", network: true } })?.network;
    const off = accessFacts({ access: { sandbox: "read-only", network: false } })?.network;
    expect(on).toBeTruthy();
    expect(off).toBeTruthy();
    expect(off).not.toBe(on);
  });

  it("says nothing when the hub doesn't report access", () => {
    expect(accessFacts({ access: undefined })).toBeNull();
  });
});
