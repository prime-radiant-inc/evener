import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { InstanceEntry } from "../../../../protocol/types.gen";
import { InstanceRow } from "./InstanceRow";

afterEach(cleanup);

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
          envVar: "ANTHROPIC_API_KEY",
          activeSource: "store",
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
    expect(screen.getByText(/default/i)).toBeTruthy();
  });
});

// implicit is the wire's own "exists from the environment, not from
// providers.toml" flag (InstanceEntry, appwire/types.go) - removal of an
// implicit instance is refused server-side (spec §11.3), so the sheet
// offers no Remove, and this badge tells the user why Edit there writes a
// shadow instead of changing the instance itself.
describe("implicit instances", () => {
  test("a 'from environment' badge marks an implicit instance", () => {
    render(
      <InstanceRow instance={instance({ name: "groq", providerId: "groq", implicit: true })} onSelect={() => {}} />,
    );
    expect(screen.getByText("from environment")).toBeTruthy();
  });

  test("a non-implicit instance carries no badge", () => {
    render(
      <InstanceRow
        instance={instance({ name: "work", providerId: "groq", base: "groq", implicit: false })}
        onSelect={() => {}}
      />,
    );
    expect(screen.queryByText("from environment")).toBeNull();
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
    expect(screen.getByText("No key set · optional · openai-chat · base http://127.0.0.1:8080/v1")).toBeTruthy();
  });
});

// The heading dot is the glyph half of what the meta line says in words, so
// the two have to agree about the same instance. StatusDot's only observable
// difference between "idle" and "ended" is its accessible name (both states
// share the neutral token family - src/widgets/statusdot), so that name is
// what these assert on.
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
    expect(screen.getByText(/No key set · optional/)).toBeTruthy();
    expect(screen.getByRole("img", { name: "Idle" })).toBeTruthy();
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
    expect(screen.getByText(/No credentials required/)).toBeTruthy();
    expect(screen.getByRole("img", { name: "Idle" })).toBeTruthy();
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
    expect(screen.getByText(/Not configured/)).toBeTruthy();
    expect(screen.getByRole("img", { name: "Ended" })).toBeTruthy();
  });

  test("a configured instance shows the idle dot", () => {
    render(
      <InstanceRow
        instance={instance({ name: "a", providerId: "x", hasStoredFile: true, activeSource: "store" })}
        onSelect={() => {}}
      />,
    );
    expect(screen.getByRole("img", { name: "Idle" })).toBeTruthy();
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
