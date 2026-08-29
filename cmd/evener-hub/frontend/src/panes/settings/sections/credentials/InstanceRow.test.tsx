import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { InstanceEntry } from "../../../../protocol/types.gen";
import { InstanceRow } from "./InstanceRow";

afterEach(cleanup);

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "type">): InstanceEntry {
  return {
    apiStyle: "",
    baseUrl: "",
    isDefault: false,
    activeSource: "absent",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
}

describe("the row carries identity and status only", () => {
  test("the whole row is one button showing name, meta, and a chevron", () => {
    render(
      <InstanceRow
        instance={instance({ name: "openai-work", type: "openai", hasStoredFile: true, activeSource: "file" })}
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
          type: "openai",
          authModes: ["apiKey", "oauth"],
          hasStoredFile: true,
          activeSource: "file",
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
          type: "openai",
          hasStoredOAuth: true,
          hasStoredFile: true,
          activeSource: "oauth",
        })}
        onSelect={() => {}}
      />,
    );
    expect(screen.queryByText("effective")).toBeNull();
    expect(screen.queryByText("shadowed")).toBeNull();
    expect(screen.queryByRole("status")).toBeNull();
  });

  test("the default badge shows when isDefault", () => {
    render(<InstanceRow instance={instance({ name: "a", type: "x", isDefault: true })} onSelect={() => {}} />);
    expect(screen.getByText(/default/i)).toBeTruthy();
  });
});

describe("the meta line", () => {
  test("apiStyle/baseUrl trailing text: apiStyle preferred, then base url", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "a",
          type: "openai",
          apiStyle: "responses",
          baseUrl: "https://x",
          hasStoredFile: true,
          activeSource: "file",
        })}
        onSelect={() => {}}
      />,
    );
    expect(screen.getByText("responses · base https://x")).toBeTruthy();
  });

  test("base url alone when apiStyle is empty", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "a",
          type: "openai",
          baseUrl: "https://x",
          hasStoredFile: true,
          activeSource: "file",
        })}
        onSelect={() => {}}
      />,
    );
    expect(screen.getByText("base https://x")).toBeTruthy();
  });

  test("the unconfigured label wins the meta line, with style info appended when present", () => {
    render(
      <InstanceRow
        instance={instance({
          name: "llama",
          type: "openai",
          baseUrl: "http://127.0.0.1:8080/v1",
          activeSource: "absent",
          credentialRequired: false,
        })}
        onSelect={() => {}}
      />,
    );
    expect(screen.getByText("No key set · optional · base http://127.0.0.1:8080/v1")).toBeTruthy();
  });

  test("an unconfigured instance with no style info shows just the label", () => {
    render(<InstanceRow instance={instance({ name: "a", type: "x", activeSource: "absent" })} onSelect={() => {}} />);
    expect(screen.getByText("Not configured")).toBeTruthy();
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
          type: "openai",
          baseUrl: "http://127.0.0.1:8080/v1",
          activeSource: "absent",
          credentialRequired: false,
        })}
        onSelect={() => {}}
      />,
    );
    expect(screen.getByRole("img", { name: "Idle" })).toBeTruthy();
  });

  test("an auth-none provider - one that authenticates nothing - is not announced as ended", () => {
    render(
      <InstanceRow
        instance={instance({ name: "ollama", type: "ollama", activeSource: "none", credentialRequired: false })}
        onSelect={() => {}}
      />,
    );
    expect(screen.getByText("No credentials required")).toBeTruthy();
    expect(screen.getByRole("img", { name: "Idle" })).toBeTruthy();
  });

  test("a provider whose required key is missing keeps the ended dot", () => {
    render(
      <InstanceRow
        instance={instance({ name: "a", type: "anthropic", activeSource: "absent", credentialRequired: true })}
        onSelect={() => {}}
      />,
    );
    expect(screen.getByRole("img", { name: "Ended" })).toBeTruthy();
  });

  test("a configured instance shows the idle dot", () => {
    render(
      <InstanceRow
        instance={instance({ name: "a", type: "x", hasStoredFile: true, activeSource: "file" })}
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
    render(<InstanceRow instance={instance({ name: "openai-work", type: "openai" })} onSelect={onSelect} />);
    await user.click(screen.getByRole("button", { name: /openai-work/ }));
    expect(onSelect).toHaveBeenCalledTimes(1);
  });
});
