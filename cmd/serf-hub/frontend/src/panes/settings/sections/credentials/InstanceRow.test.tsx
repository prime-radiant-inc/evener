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
    ...overrides,
  };
}

function noopHandlers() {
  return {
    onTestCredentials: vi.fn(),
    onSetApiKey: vi.fn(),
    onOAuthStart: vi.fn(),
    onEdit: vi.fn(),
    onClear: vi.fn(),
    onRemove: vi.fn(),
    onSetDefault: vi.fn(),
  };
}

describe("row actions are conditionally rendered", () => {
  test("Set key only when authModes includes apiKey", () => {
    const handlers = noopHandlers();
    render(<InstanceRow instance={instance({ name: "a", type: "x", authModes: ["oauth"] })} {...handlers} />);
    expect(screen.queryByRole("button", { name: /set key|replace key/i })).toBeNull();
  });

  test("Sign in… only when authModes includes oauth", () => {
    const handlers = noopHandlers();
    render(<InstanceRow instance={instance({ name: "a", type: "x", authModes: ["apiKey"] })} {...handlers} />);
    expect(screen.queryByRole("button", { name: /sign in|refresh oauth/i })).toBeNull();
  });

  test("Clear only when activeSource is file or oauth", () => {
    const handlers = noopHandlers();
    const { rerender } = render(
      <InstanceRow instance={instance({ name: "a", type: "x", activeSource: "env" })} {...handlers} />,
    );
    expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
    rerender(
      <InstanceRow
        instance={instance({ name: "a", type: "x", activeSource: "file", hasStoredFile: true })}
        {...handlers}
      />,
    );
    expect(screen.getByRole("button", { name: "Clear" })).toBeTruthy();
  });

  test("Edit and Remove are always present", () => {
    const handlers = noopHandlers();
    render(<InstanceRow instance={instance({ name: "a", type: "x" })} {...handlers} />);
    expect(screen.getByRole("button", { name: "Edit" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
  });

  test("make default only when not already default", () => {
    const handlers = noopHandlers();
    const { rerender } = render(
      <InstanceRow instance={instance({ name: "a", type: "x", isDefault: false })} {...handlers} />,
    );
    expect(screen.getByRole("button", { name: /make default/i })).toBeTruthy();
    rerender(<InstanceRow instance={instance({ name: "a", type: "x", isDefault: true })} {...handlers} />);
    expect(screen.queryByRole("button", { name: /make default/i })).toBeNull();
  });
});

describe("Set/Replace key label", () => {
  test("'Set key' when no file-sourced key exists", () => {
    const handlers = noopHandlers();
    render(
      <InstanceRow
        instance={instance({ name: "a", type: "x", authModes: ["apiKey"], hasStoredFile: false })}
        {...handlers}
      />,
    );
    expect(screen.getByRole("button", { name: "Set key" })).toBeTruthy();
  });

  test("'Replace key' whenever a file-sourced key exists, even if a different source is currently effective", () => {
    const handlers = noopHandlers();
    render(
      <InstanceRow
        instance={instance({
          name: "a",
          type: "x",
          authModes: ["apiKey"],
          hasStoredFile: true,
          hasStoredOAuth: true,
          activeSource: "oauth",
        })}
        {...handlers}
      />,
    );
    expect(screen.getByRole("button", { name: "Replace key" })).toBeTruthy();
  });
});

describe("Sign in / Refresh OAuth label", () => {
  test("'Sign in…' when no OAuth is stored", () => {
    const handlers = noopHandlers();
    render(
      <InstanceRow
        instance={instance({ name: "a", type: "x", authModes: ["oauth"], hasStoredOAuth: false })}
        {...handlers}
      />,
    );
    expect(screen.getByRole("button", { name: "Sign in…" })).toBeTruthy();
  });

  test("'Refresh OAuth' once signed in", () => {
    const handlers = noopHandlers();
    render(
      <InstanceRow
        instance={instance({ name: "a", type: "x", authModes: ["oauth"], hasStoredOAuth: true })}
        {...handlers}
      />,
    );
    expect(screen.getByRole("button", { name: "Refresh OAuth" })).toBeTruthy();
  });
});

describe("layered credential display", () => {
  test("an unconfigured instance shows 'Not configured' and no Clear button", () => {
    const handlers = noopHandlers();
    render(<InstanceRow instance={instance({ name: "a", type: "x", activeSource: "absent" })} {...handlers} />);
    expect(screen.getByText("Not configured")).toBeTruthy();
  });

  test("multiple layers show effective + shadowed badges", () => {
    const handlers = noopHandlers();
    render(
      <InstanceRow
        instance={instance({
          name: "a",
          type: "openai",
          hasStoredOAuth: true,
          hasStoredFile: true,
          activeSource: "oauth",
        })}
        {...handlers}
      />,
    );
    expect(screen.getByText("effective")).toBeTruthy();
    expect(screen.getByText("shadowed")).toBeTruthy();
  });

  test("apiStyle/baseUrl trailing text: apiStyle preferred, then base url", () => {
    const handlers = noopHandlers();
    render(
      <InstanceRow
        instance={instance({ name: "a", type: "openai", apiStyle: "responses", baseUrl: "https://x" })}
        {...handlers}
      />,
    );
    expect(screen.getByText("responses · base https://x")).toBeTruthy();
  });

  test("base url alone when apiStyle is empty", () => {
    const handlers = noopHandlers();
    render(<InstanceRow instance={instance({ name: "a", type: "openai", baseUrl: "https://x" })} {...handlers} />);
    expect(screen.getByText("base https://x")).toBeTruthy();
  });

  test("the default badge shows when isDefault", () => {
    const handlers = noopHandlers();
    render(<InstanceRow instance={instance({ name: "a", type: "x", isDefault: true })} {...handlers} />);
    expect(screen.getByText(/default/i)).toBeTruthy();
  });
});

describe("action callbacks fire", () => {
  test("clicking each action calls its handler", async () => {
    const handlers = noopHandlers();
    const user = userEvent.setup();
    render(
      <InstanceRow
        instance={instance({
          name: "a",
          type: "openai",
          authModes: ["apiKey", "oauth"],
          hasStoredFile: true,
          activeSource: "file",
        })}
        {...handlers}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Replace key" }));
    expect(handlers.onSetApiKey).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Sign in…" }));
    expect(handlers.onOAuthStart).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Clear" }));
    expect(handlers.onClear).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(handlers.onEdit).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(handlers.onRemove).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: /make default/i }));
    expect(handlers.onSetDefault).toHaveBeenCalled();
  });

  test("clicking Test credentials calls its handler", async () => {
    const handlers = noopHandlers();
    const user = userEvent.setup();
    render(<InstanceRow instance={instance({ name: "a", type: "x" })} {...handlers} />);

    await user.click(screen.getByRole("button", { name: "Test credentials" }));

    expect(handlers.onTestCredentials).toHaveBeenCalledTimes(1);
  });

  test("pending verification disables only the Test credentials action", () => {
    const handlers = noopHandlers();
    render(<InstanceRow instance={instance({ name: "a", type: "x" })} {...handlers} testCredentialsPending />);

    expect((screen.getByRole("button", { name: "Testing credentials…" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Edit" }) as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
  });
});
