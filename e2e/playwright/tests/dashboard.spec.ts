import { expect, test } from "@playwright/test";

test("dashboard renders and CSS asset is reachable", async ({ page, request }) => {
  const response = await request.get("/assets/app.css");
  expect(response.ok()).toBeTruthy();

  await page.goto("/");
  await expect(page.getByTestId("dashboard-root")).toBeVisible();
  const stateCounts = page.getByTestId("linear-state-counts");
  await expect(stateCounts).toBeVisible();
  await expect(stateCounts.getByText("In Progress")).toBeVisible();
  await expect(page.getByTestId("worker-card-COLIN-7")).toBeVisible();
});

test("worker card expands and refresh updates the fragment without reloading the shell", async ({ page }) => {
  await page.goto("/");

  const shellInstance = await page.getByTestId("shell-instance").textContent();
  const before = await page.getByTestId("turn-count-COLIN-7").textContent();

  await page.getByText("Codex output").click();
  await expect(page.getByTestId("worker-output-COLIN-7")).toContainText("Refreshed the task view fragment.");

  await page.getByTestId("refresh-button").click();
  await expect(page.getByTestId("turn-count-COLIN-7")).not.toHaveText(before ?? "");

  const afterShellInstance = await page.getByTestId("shell-instance").textContent();
  expect(afterShellInstance).toBe(shellInstance);
});
