import { test, expect } from "@playwright/test";

test("Mermaid fences render SVG on initial load and client navigation", async ({
  page,
}) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("http://127.0.0.1:4174/evener/");
  await expect(page.locator(".vp-doc svg .node")).toHaveCount(3);
  await expect(page.locator(".vp-doc svg")).toBeVisible();
  await page.getByRole("link", { name: "Next page", exact: true }).click();
  await expect(page).toHaveURL(/next/);
  await expect(page.locator(".vp-doc svg .messageLine0")).toHaveCount(1);
  await expect(page.locator(".vp-doc svg")).toBeVisible();
  await page.goBack();
  await expect(page.locator(".vp-doc svg .node")).toHaveCount(3);
  expect(errors).toEqual([]);
});
