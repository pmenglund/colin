import { expect, test } from "@playwright/test";

test("dashboard renders and CSS asset is reachable", async ({ page, request }) => {
  const response = await request.get("/assets/app.css");
  expect(response.ok()).toBeTruthy();

  await page.goto("/");
  await expect(page.getByTestId("dashboard-root")).toBeVisible();
  const stateCounts = page.getByTestId("linear-state-counts");
  await expect(stateCounts).toBeVisible();
  await expect(stateCounts.locator(".stat-title").filter({ hasText: /^In Progress$/ })).toBeVisible();
  await expect(page.getByTestId("paused-issues-review")).toBeVisible();
  await expect(page.getByTestId("paused-issues-review")).toHaveAttribute(
    "href",
    "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22",
  );
  await page.getByTestId("state-issues-trigger-in-progress").click();
  await expect(page.getByTestId("state-issues-in-progress")).toContainText("COLIN-7");
  await expect(page.getByTestId("state-issues-in-progress")).toContainText("Issue ID");
  await expect(page.getByTestId("state-issues-in-progress")).toContainText("Title");
  await expect
    .poll(async () => page.getByTestId("linear-state-counts").evaluate((element) => getComputedStyle(element).zIndex))
    .toBe("20");
  await expect(page.getByTestId("state-issue-in-progress-COLIN-7").getByRole("link", { name: "COLIN-7" })).toHaveAttribute(
    "href",
    "https://linear.app/example/issue/COLIN-7",
  );
  await expect(page.getByTestId("state-issue-in-progress-COLIN-7").getByRole("link", { name: "Render live dashboard cards" })).toHaveAttribute(
    "href",
    "/linear/issues/issue-demo-1/metadata",
  );
  await expect(page.getByTestId("worker-card-COLIN-7")).toBeVisible();
  await expect(page.getByTestId("context-window-COLIN-7")).toHaveText("Context window: 70% left (78.4K used / 258K)");
  await expect(page.getByTestId("context-window-bar-COLIN-7")).toHaveAttribute("aria-valuenow", "30");
  await expect(page.getByTestId("shell-instance")).toHaveCount(0);
});

test("worker card expands and refresh updates the fragment while preserving expanded state", async ({ page }) => {
  await page.goto("/");

  const before = await page.getByTestId("turn-count-COLIN-7").textContent();
  const details = page.locator("#worker-output-details-COLIN-7");
  const stateIssuesDetails = page.locator("#state-issues-details-in-progress");
  const outputPre = page.getByTestId("worker-output-COLIN-7").locator("pre").first();
  const outputTime = page.getByTestId("worker-output-COLIN-7").locator("[data-local-time]").first();

  await page.getByText("Codex output").click();
  await expect(details).toHaveJSProperty("open", true);
  await page.getByTestId("state-issues-trigger-in-progress").click();
  await expect(stateIssuesDetails).toHaveJSProperty("open", true);
  await expect(page.getByTestId("worker-output-COLIN-7")).toContainText("Refreshed the task view fragment.");
  await expect(outputPre).toHaveCSS("white-space", "pre-wrap");
  const timestamp = await outputTime.getAttribute("data-timestamp");
  if (!timestamp) {
    throw new Error("missing output timestamp");
  }
  const expectedLocalTime = await page.evaluate((value) => {
    return new Intl.DateTimeFormat(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      timeZoneName: "short",
    }).format(new Date(value));
  }, timestamp);
  await expect(outputTime).toHaveText(expectedLocalTime);

  await page.getByTestId("refresh-button").click();
  await expect(page.getByTestId("refresh-button")).toHaveAttribute("aria-label", "Resume automatic refresh");
  await page.getByTestId("refresh-button").click();
  await expect(page.getByTestId("turn-count-COLIN-7")).not.toHaveText(before ?? "");
  await expect(details).toHaveJSProperty("open", true);
  await expect(stateIssuesDetails).toHaveJSProperty("open", true);
  const refreshedTimestamp = await outputTime.getAttribute("data-timestamp");
  if (!refreshedTimestamp) {
    throw new Error("missing refreshed output timestamp");
  }
  const expectedRefreshedLocalTime = await page.evaluate((value) => {
    return new Intl.DateTimeFormat(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      timeZoneName: "short",
    }).format(new Date(value));
  }, refreshedTimestamp);
  await expect(outputTime).toHaveText(expectedRefreshedLocalTime);
});

test("dashboard marks the view stale when refresh fails and recovers after a later success", async ({ page }) => {
  await page.goto("/");

  const refreshStatus = page.getByTestId("refresh-status");
  await expect(refreshStatus).toHaveAttribute("data-refresh-status", "live");
  const generatedAt = await refreshStatus.getAttribute("data-generated-at");
  expect(generatedAt).toBeTruthy();

  await page.getByTestId("refresh-button").click();
  await expect(page.getByTestId("refresh-button")).toHaveAttribute("aria-label", "Resume automatic refresh");

  let failNextRefresh = true;
  const routeHandler = async (route) => {
    const request = route.request();
    const headers = request.headers();
    const path = new URL(request.url()).pathname;
    if (failNextRefresh && headers["hx-request"] === "true" && path === "/") {
      failNextRefresh = false;
      await route.abort("failed");
      return;
    }
    await route.continue();
  };
  await page.route("**/*", routeHandler);

  await page.getByTestId("refresh-button").click();
  await expect(refreshStatus).toHaveAttribute("data-refresh-status", "stale");
  await expect(refreshStatus).toHaveText("Stale data");
  await expect(refreshStatus).toHaveAttribute("data-generated-at", generatedAt ?? "");
  await expect(page.getByTestId("worker-card-COLIN-7")).toBeVisible();

  await page.getByTestId("refresh-button").click();
  await expect(page.getByTestId("refresh-button")).toHaveAttribute("aria-label", "Resume automatic refresh");
  await page.getByTestId("refresh-button").click();
  await expect(refreshStatus).toHaveAttribute("data-refresh-status", "live");
  await expect(refreshStatus).toHaveText("Live data");

  await page.unroute("**/*", routeHandler);
});
