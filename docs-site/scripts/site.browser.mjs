import { test, expect } from "@playwright/test";
test("desktop keyboard navigation and local search use the Pages base", async ({
  page,
}) => {
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("./");
  await page.keyboard.press("Tab");
  await expect(page.locator(":focus")).toHaveAttribute("href", "#VPContent");
  await page.getByRole("button", { name: "Search", exact: false }).click();
  const input = page.getByRole("searchbox");
  await expect(input).toBeVisible();
  await input.fill("AppWire");
  await expect(
    page.locator('.VPLocalSearchBox a[href*="/reference/appwire"]').first(),
  ).toBeVisible();
  await input.press("ArrowDown");
  await input.press("Enter");
  await expect(page).toHaveURL(/\/evener\/reference\/appwire/);
  await expect(page.locator(".vp-doc table").first()).toBeVisible();
  expect(errors).toEqual([]);
});
test("mobile navigation fits the viewport and opens the reference", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("./");
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.getByRole("button", { name: "Menu", exact: true }).click();
  await page
    .locator(".VPSidebar")
    .getByRole("link", { name: "AppWire protocol", exact: true })
    .click();
  await expect(page).toHaveURL(/\/evener\/reference\/appwire/);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});
