import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, expect, test } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { toastKinds } from "../../../widgets/toast/toastTestUtils";
import { DisplaySection } from "./display";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  localStorage.clear();
  resetPrefsStoreForTests();
  resetToastStoreForTests();
});

function renderWithToasts() {
  render(
    <>
      <DisplaySection />
      <Toast />
    </>,
  );
}

test("Enter sends remains the only Display setting and defaults off", () => {
  renderWithToasts();
  expect(screen.getByRole("switch", { name: "Enter sends" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getAllByRole("switch")).toHaveLength(1);
});

test("toggling Enter sends persists to the pinned key and toasts Settings saved", async () => {
  const user = userEvent.setup();
  renderWithToasts();

  await user.click(screen.getByRole("switch", { name: "Enter sends" }));

  expect(prefsStore.getState().enterToSend).toBe(true);
  expect(localStorage.getItem("evener.prefs.enterToSend")).toBe("1");
  await waitFor(() => expect(toastKinds()).toEqual(["success"]));
});
