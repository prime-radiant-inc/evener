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
  const options = page.locator('.VPLocalSearchBox [role="option"]');
  await expect(options.first().locator("a")).toHaveAttribute(
    "href",
    /\/evener\/reference\/appwire\.html#/,
  );
  await expect(options.first()).toHaveAttribute("aria-selected", "true");
  await input.press("ArrowDown");
  await expect(options.nth(1)).toHaveAttribute("aria-selected", "true");
  await input.press("ArrowUp");
  await expect(options.first()).toHaveAttribute("aria-selected", "true");
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

for (const width of [1200, 320, 390]) {
  for (const input of ["pointer", "keyboard"]) {
    test(
      `Job control tool index navigates by ${input} at ${width}px`,
      async ({ page }) => {
        await page.setViewportSize({ width, height: 844 });
        for (const id of ["job_stop", "job_status", "job_watch"]) {
          await page.goto("./job-control.html");
          await expect(page).toHaveURL(/\/evener\/job-control\.html$/);
          await page.evaluate(() => scrollTo(0, 0));
          const index = page.locator(".vp-doc #model-facing-tools + p");
          const link = index.locator(`a[href$="#${id}"]`);
          await expect(link).toHaveCount(1);
          if (input === "pointer") {
            await link.click();
          } else {
            await page.keyboard.press("Tab");
            for (
              let step = 0;
              step < 160 &&
              !(await link.evaluate((el) => el === document.activeElement));
              step++
            ) {
              await page.keyboard.press("Tab");
            }
            await expect(link).toBeFocused();
            await page.keyboard.press("Enter");
          }
          await expect(page).toHaveURL(
            new RegExp(`/evener/job-control\\.html#${id}$`),
          );
          await expect
            .poll(() =>
              page.evaluate((target) => {
                const rect = document
                  .getElementById(target)
                  .getBoundingClientRect();
                const stickyBottom = [
                  ...document.querySelectorAll(".VPNav, .VPLocalNav"),
                ]
                  .filter((el) => {
                    const position = getComputedStyle(el).position;
                    const bounds = el.getBoundingClientRect();
                    return (
                      (position === "fixed" || position === "sticky") &&
                      bounds.top < innerHeight &&
                      bounds.bottom > 0
                    );
                  })
                  .reduce(
                    (bottom, el) =>
                      Math.max(bottom, el.getBoundingClientRect().bottom),
                    0,
                  );
                return (
                  stickyBottom > 0 &&
                  rect.top >= stickyBottom - 1 &&
                  rect.top < innerHeight
                );
              }, id),
            )
            .toBe(true);
        }
      },
    );
  }
}
