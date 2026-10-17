import { test, expect } from "@playwright/test";

// Mermaid's async import and layout continue after navigation's load event.
// Observe its completed DOM replacement instead of spending the SVG assertion's
// five-second retry window waiting for that work. The test timeout stays intact.
async function waitForDiagrams(page) {
  await page.evaluate(
    () =>
      new Promise((resolve) => {
        const complete = () =>
          document.querySelector(".vp-doc .mermaid-diagram") &&
          !document.querySelector(".vp-doc .language-mermaid");
        if (complete()) {
          resolve();
          return;
        }
        const observer = new MutationObserver(() => {
          if (complete()) {
            observer.disconnect();
            resolve();
          }
        });
        observer.observe(document.documentElement, {
          childList: true,
          subtree: true,
        });
      }),
  );
}

test("Mermaid fences render SVG on initial load and client navigation", async ({
  page,
}) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("http://127.0.0.1:4174/evener/");
  await waitForDiagrams(page);
  await expect(page.locator(".vp-doc svg .node")).toHaveCount(3);
  await expect(page.locator(".vp-doc svg")).toBeVisible();
  await page.getByRole("link", { name: "Next page", exact: true }).click();
  await expect(page).toHaveURL(/next/);
  await waitForDiagrams(page);
  await expect(page.locator(".vp-doc svg .messageLine0")).toHaveCount(1);
  await expect(page.locator(".vp-doc svg")).toBeVisible();
  await page.goBack();
  await waitForDiagrams(page);
  await expect(page.locator(".vp-doc svg .node")).toHaveCount(3);
  expect(errors).toEqual([]);
});
