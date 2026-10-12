import type { InstanceEntry } from "@evener/appwire-client";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { InstanceRow } from "./InstanceRow";

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  return {
    protocol: "openai-chat",
    auth: "bearer",
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
}

// The row's credential state (configured, keyless, missing, error), the chips it
// carries by kind, and its heading dot's state, read from markers rather than
// from the words the row shows for them.
function rowState(): string | null {
  const marked = document.querySelector("[data-credential-state]");
  if (!marked) throw new Error("Missing row credential state");
  return marked.getAttribute("data-credential-state");
}

function chipKinds(): (string | null)[] {
  return [...document.querySelectorAll("[data-kind]")].map((chip) => chip.getAttribute("data-kind"));
}

function dotState(): string | null {
  return screen.getByRole("img").getAttribute("data-state");
}

describe("the row carries identity and status only", () => {
  test("the whole row is one button showing name, meta, and a chevron", () => {
    render(
      <InstanceRow
        instance={instance({ name: "openai-work", providerId: "openai", hasStoredFile: true, activeSource: "store" })}
        onSelect={() => {}}
      />,
    );
    const row = screen.getByRole("button", { name: /openai-work/ });
    expect(row).toBeTruthy();
  });

  test("rows carry no per-row action buttons - every action lives in the detail sheet", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "a",
          providerId: "openai",
          authModes: ["apiKey"],
          hasStoredFile: true,
          activeSource: "store",
        })}
        onSelect={() => {}}
      />,
    );
    for (const name of ["Edit", "Remove", "Clear", "Set key", "Sign in…", "Test credentials"]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
    expect(screen.queryByRole("button", { name: /make default/i })).toBeNull();
  });

  test("the layered credential chips and the test result line live in the sheet, not the row", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "a",
          providerId: "anthropic",
          hasStoredFile: true,
          activeSource: "api_key",
        })}
        onSelect={() => {}}
      />,
    );
    expect(screen.queryByText("effective")).toBeNull();
    expect(screen.queryByText("shadowed")).toBeNull();
    expect(screen.queryByRole("status")).toBeNull();
  });

  test("the default badge shows when isDefault", () => {
    render(<InstanceRow instance={instance({ name: "a", providerId: "x", isDefault: true })} onSelect={() => {}} />);
    expect(chipKinds()).toEqual(["default"]);
  });
});

// The badge names where the instance's credential comes from, not merely
// that no providers.toml entry shadows it: an implicit instance the user
// signed in to or stored a key for is their own, and the badge would name a
// source it never read.
describe("environment-backed instances", () => {
  test("a 'from environment' badge marks an instance an environment variable supplies", () => {
    render(
      <InstanceRow
        instance={instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "env:GROQ_API_KEY" })}
        onSelect={() => {}}
      />,
    );
    expect(chipKinds()).toEqual(["environment"]);
  });

  test("a 'from environment' badge marks an instance the ADC file supplies", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "vertex",
          providerId: "google-vertex",
          implicit: true,
          activeSource: "adc",
          auth: "gcp-adc",
        })}
        onSelect={() => {}}
      />,
    );
    expect(chipKinds()).toEqual(["environment"]);
  });

  test("a stored key through the UI carries no badge", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "groq",
          providerId: "groq",
          implicit: true,
          activeSource: "store",
          hasStoredFile: true,
        })}
        onSelect={() => {}}
      />,
    );
    expect(chipKinds()).not.toContain("environment");
  });

  test("a signed-in Codex account carries no badge", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "openai-codex",
          providerId: "openai-codex",
          auth: "oauth-openai-codex",
          implicit: true,
          activeSource: "oauth",
          hasStoredOAuth: true,
        })}
        onSelect={() => {}}
      />,
    );
    expect(chipKinds()).not.toContain("environment");
  });

  test("a non-implicit instance carries no badge", () => {
    render(
      <InstanceRow
        instance={instance({ name: "work", providerId: "groq", base: "groq", implicit: false })}
        onSelect={() => {}}
      />,
    );
    expect(chipKinds()).not.toContain("environment");
  });
});

describe("the meta line", () => {
  test("protocol and base URL trailing text", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "a",
          providerId: "openai",
          protocol: "openai-responses",
          baseUrl: "https://x",
          hasStoredFile: true,
          activeSource: "store",
        })}
        onSelect={() => {}}
      />,
    );
    expect(screen.getByText("openai-responses · base https://x")).toBeTruthy();
  });

  test("protocol alone when baseUrl is empty", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "a",
          providerId: "openai",
          protocol: "openai-chat",
          hasStoredFile: true,
          activeSource: "store",
        })}
        onSelect={() => {}}
      />,
    );
    expect(screen.getByText("openai-chat")).toBeTruthy();
  });

  test("the unconfigured label wins the meta line, with the protocol/base URL appended", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "llama",
          providerId: "openai-compatible",
          auth: "optional-bearer",
          baseUrl: "http://127.0.0.1:8080/v1",
          activeSource: "none",
          credentialRequired: false,
        })}
        onSelect={() => {}}
      />,
    );
    // The meta line leads with the unconfigured state's words, ahead of the
    // endpoint the fixture gave it.
    expect(rowState()).toBe("keyless");
    const meta = screen.getByRole("button", { name: /llama/ }).textContent ?? "";
    expect(meta).toContain("http://127.0.0.1:8080/v1");
    expect(meta.indexOf("openai-chat")).toBeGreaterThan(meta.indexOf("llama") + "llama".length);
  });
});

// The heading dot is the glyph half of what the meta line says in words, so
// the two have to agree about the same instance. StatusDot's only observable
// difference between "idle" and "ended" is its accessible name (both states
// share the neutral token family - src/widgets/statusdot), so its data-state is
// what these assert on, beside the row's own credential state.
describe("the heading dot agrees with the meta line", () => {
  test("a keyless gateway - no key, none needed - is not announced as ended", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "llama",
          providerId: "openai-compatible",
          auth: "optional-bearer",
          activeSource: "none",
          credentialRequired: false,
        })}
        onSelect={() => {}}
      />,
    );
    expect(rowState()).toBe("keyless");
    expect(dotState()).toBe("idle");
  });

  test("an auth-none provider - one that authenticates nothing - is not announced as ended", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "ollama",
          providerId: "ollama",
          auth: "none",
          activeSource: "none",
          credentialRequired: false,
        })}
        onSelect={() => {}}
      />,
    );
    expect(rowState()).toBe("keyless");
    expect(dotState()).toBe("idle");
  });

  test("a provider whose required key is missing keeps the ended dot", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "a",
          providerId: "anthropic",
          auth: "bearer",
          activeSource: "none",
          credentialRequired: true,
        })}
        onSelect={() => {}}
      />,
    );
    expect(rowState()).toBe("missing");
    expect(dotState()).toBe("ended");
  });

  test("a configured instance shows the idle dot", () => {
    render(
      <InstanceRow
        instance={instance({ name: "a", providerId: "x", hasStoredFile: true, activeSource: "store" })}
        onSelect={() => {}}
      />,
    );
    expect(rowState()).toBe("configured");
    expect(dotState()).toBe("idle");
  });
});

describe("selection", () => {
  test("clicking the row calls onSelect", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<InstanceRow instance={instance({ name: "openai-work", providerId: "openai" })} onSelect={onSelect} />);
    await user.click(screen.getByRole("button", { name: /openai-work/ }));
    expect(onSelect).toHaveBeenCalledTimes(1);
  });
});

// The host-scoped view of a remote host's own listing renders this row
// read-only: the same identity and meta (dot, name, chips, meta line) with no
// button and no chevron, because nothing on a remote host's row is actionable
// from this browser.
describe("the read-only variant", () => {
  test("renders the same identity and meta with no button", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "on-host",
          providerId: "anthropic",
          isDefault: true,
          hasStoredFile: true,
          activeSource: "store",
        })}
        readOnly
      />,
    );
    expect(screen.getByText("on-host")).toBeTruthy();
    expect(chipKinds()).toEqual(["default"]);
    expect(rowState()).toBe("configured");
    expect(screen.getByText("openai-chat")).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });

  // Low (roborev): the read-only branch returned a bare <li>, so a remote row
  // lost the row chrome entirely (padding, edge, radius, surface background, the
  // 44px phone floor) while the same instance was a surface row locally. Both
  // variants are the same ListRow, the tappable one adding only its
  // interactive modifier.
  test("the read-only row carries all of the chrome the tappable row carries", () => {
    const readOnly = render(<InstanceRow instance={instance({ name: "on-host", providerId: "anthropic" })} readOnly />);
    const readOnlyClasses = readOnly.container.querySelector("li > *")?.className.split(" ") ?? [];
    readOnly.unmount();
    render(<InstanceRow instance={instance({ name: "on-host", providerId: "anthropic" })} onSelect={() => {}} />);
    const buttonClasses = screen.getByRole("button", { name: /on-host/ }).className.split(" ");
    expect(readOnlyClasses.length).toBeGreaterThan(0);
    expect(buttonClasses).toEqual(expect.arrayContaining(readOnlyClasses));
  });
});

// L-2: the contract is in the props type, not in a comment. A row that is not
// read-only must carry onSelect, or an interactive button renders with no
// handler. This is a compile-time assertion checked by tsc: if the props type
// stops requiring onSelect, the directive below is unused and tsc fails.
test("the props type requires onSelect whenever the row is not read-only", () => {
  // @ts-expect-error a row that is not readOnly must be given onSelect
  render(<InstanceRow instance={instance({ name: "on-host", providerId: "anthropic" })} />);
});

// A credential the provider rejected (#3539): the row is in the error state, with the
// failed dot, so the list shows which provider needs its key replaced.
describe("a provider whose credential the hub found an error with", () => {
  test("is in the error state with the failed dot", () => {
    render(
      <InstanceRow
        instance={instance({ name: "lunaroute", providerId: "openai", hasStoredFile: true, activeSource: "store" })}
        authError="The provider rejected this credential (HTTP 401). Replace the key or sign in again."
        onSelect={() => {}}
      />,
    );
    expect(rowState()).toBe("error");
    expect(dotState()).toBe("failed");
  });

  test("a provider with no error reads as before", () => {
    render(
      <InstanceRow
        instance={instance({ name: "lunaroute", providerId: "openai", hasStoredFile: true, activeSource: "store" })}
        onSelect={() => {}}
      />,
    );
    expect(rowState()).toBe("configured");
    expect(dotState()).toBe("idle");
  });
});
