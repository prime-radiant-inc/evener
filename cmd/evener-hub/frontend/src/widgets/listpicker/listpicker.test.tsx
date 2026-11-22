import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, test, vi } from "vitest";
import { Button } from "../button";
import { ListPicker, type ListPickerOption } from "./index";

function Harness({ onFooter = vi.fn() }: { onFooter?: () => void }) {
  const [value, setValue] = useState("local");
  const options: ListPickerOption[] = [
    {
      id: "local",
      label: "magic-kingdom (hub)",
      onSelect: () => setValue("local"),
    },
    {
      id: "buildbox",
      label: "buildbox",
      onSelect: () => setValue("buildbox"),
    },
    {
      id: "offline-host",
      label: "offline-host",
      meta: "Offline",
      action: (
        <Button variant="quiet" size="xs" type="button" aria-label="Connect offline-host">
          Connect
        </Button>
      ),
    },
  ];
  return (
    <ListPicker
      id="host"
      label="Host"
      value={value}
      options={options}
      footer={{ label: "Connect host", onSelect: onFooter }}
    />
  );
}

function trigger(): HTMLButtonElement {
  return screen.getByRole("button", { name: /^Host:/ }) as HTMLButtonElement;
}

async function open(user: ReturnType<typeof userEvent.setup>): Promise<HTMLElement> {
  await user.click(trigger());
  return screen.getByRole("group", { name: "Host options" });
}

test("the closed trigger shows the chosen option and names the field", () => {
  render(<Harness />);
  expect(trigger().getAttribute("aria-label")).toBe("Host: magic-kingdom (hub)");
  expect(within(trigger()).getByText("magic-kingdom (hub)")).toBeTruthy();
});

test("emptyLabel stands in when value matches no option", () => {
  render(<ListPicker id="host" label="Host" value="gone" options={[]} emptyLabel="Choose a host" />);
  expect(trigger().getAttribute("aria-label")).toBe("Host: Choose a host");
});

describe("open list", () => {
  test("shows every option in one list and marks the current one", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const panel = await open(user);

    expect(within(panel).getByRole("button", { name: "magic-kingdom (hub)" }).getAttribute("aria-current")).toBe(
      "true",
    );
    expect(within(panel).getByRole("button", { name: "buildbox" })).toBeTruthy();
    expect(within(panel).getByText("Offline")).toBeTruthy();
    expect(within(panel).getByRole("button", { name: "Connect offline-host" })).toBeTruthy();
    expect(within(panel).getByRole("button", { name: "Connect host" })).toBeTruthy();
  });

  test("choosing a host closes the list and updates the trigger", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const panel = await open(user);

    await user.click(within(panel).getByRole("button", { name: "buildbox" }));

    expect(screen.queryByRole("group", { name: "Host options" })).toBeNull();
    expect(trigger().getAttribute("aria-label")).toBe("Host: buildbox");
  });

  test("an offline row's Connect stays open and does not change the chosen host", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const panel = await open(user);

    await user.click(within(panel).getByRole("button", { name: "Connect offline-host" }));

    expect(screen.getByRole("group", { name: "Host options" })).toBe(panel);
    expect(trigger().getAttribute("aria-label")).toBe("Host: magic-kingdom (hub)");
  });

  test("the footer action closes the list and runs without changing the chosen host", async () => {
    const user = userEvent.setup();
    const onFooter = vi.fn();
    render(<Harness onFooter={onFooter} />);
    const panel = await open(user);

    await user.click(within(panel).getByRole("button", { name: "Connect host" }));

    expect(onFooter).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("group", { name: "Host options" })).toBeNull();
    expect(trigger().getAttribute("aria-label")).toBe("Host: magic-kingdom (hub)");
  });

  test("arrow keys move through rows, row actions and the footer, then wrap", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const panel = await open(user);
    const local = within(panel).getByRole("button", { name: "magic-kingdom (hub)" });
    const buildbox = within(panel).getByRole("button", { name: "buildbox" });
    const connectOffline = within(panel).getByRole("button", { name: "Connect offline-host" });
    const connectHost = within(panel).getByRole("button", { name: "Connect host" });

    await waitFor(() => expect(document.activeElement).toBe(local));
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(buildbox);
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(connectOffline);
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(connectHost);
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(local);
    await user.keyboard("{ArrowUp}");
    expect(document.activeElement).toBe(connectHost);
    await user.keyboard("{Home}");
    expect(document.activeElement).toBe(local);
    await user.keyboard("{End}");
    expect(document.activeElement).toBe(connectHost);
  });

  test("Escape closes the list", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await open(user);

    await user.keyboard("{Escape}");

    expect(screen.queryByRole("group", { name: "Host options" })).toBeNull();
  });
});

test("a disabled trigger cannot open the list", async () => {
  const user = userEvent.setup();
  render(<ListPicker id="host" label="Host" value="local" options={[]} disabled />);

  await user.click(trigger());

  expect(screen.queryByRole("group", { name: "Host options" })).toBeNull();
});
