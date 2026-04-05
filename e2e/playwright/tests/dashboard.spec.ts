import { expect, test } from "@playwright/test";

test("dashboard renders and CSS asset is reachable", async ({ page, request }) => {
  const response = await request.get("/assets/app.css");
  expect(response.ok()).toBeTruthy();

  await page.goto("/");
  await page.getByTestId("refresh-button").click();
  await expect(page.getByTestId("refresh-button")).toHaveAttribute("aria-label", "Resume automatic refresh");
  await expect(page.getByTestId("dashboard-root")).toBeVisible();
  await expect(page.getByTestId("snapshot-age")).toHaveText(/old$/);
  const stateCounts = page.getByTestId("linear-state-counts");
  await expect(stateCounts).toBeVisible();
  await expect(stateCounts.locator(".stat-title").filter({ hasText: /^In Progress$/ })).toBeVisible();
  await expect(page.getByTestId("paused-issues-review")).toBeVisible();
  await expect(page.getByTestId("paused-issues-review")).toHaveAttribute(
    "href",
    "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22",
  );
  await page.getByTestId("state-issues-trigger-review").click();
  const stateIssuesReview = page.getByTestId("state-issues-review");
  await expect(stateIssuesReview).toContainText("COLIN-24");
  await expect(stateIssuesReview).toContainText("Issue ID");
  await expect(stateIssuesReview).toContainText("Title");
  const popupFontSizes = await page.evaluate(() => {
    const header = document.querySelector("[data-testid='state-issues-review'] th");
    const idLink = document.querySelector("[data-testid='state-issues-review'] .state-issue-id-link");
    const titleLink = document.querySelector("[data-testid='state-issues-review'] .state-issue-title-link");
    if (!(header instanceof HTMLElement) || !(idLink instanceof HTMLElement) || !(titleLink instanceof HTMLElement)) {
      throw new Error("missing popup table content");
    }
    return {
      header: window.getComputedStyle(header).fontSize,
      idLink: window.getComputedStyle(idLink).fontSize,
      titleLink: window.getComputedStyle(titleLink).fontSize,
    };
  });
  expect(popupFontSizes.idLink).toBe(popupFontSizes.header);
  expect(popupFontSizes.titleLink).toBe(popupFontSizes.header);
  await page.evaluate(() => {
    const popup = document.querySelector("[data-testid='state-issues-review']");
    const runningPanel = document.querySelector("[data-testid='running-panel']");
    if (!(popup instanceof HTMLElement) || !(runningPanel instanceof HTMLElement)) {
      return;
    }

    const popupRect = popup.getBoundingClientRect();
    const runningRect = runningPanel.getBoundingClientRect();
    const desiredTop = popupRect.top + 24;
    const shift = runningRect.top - desiredTop;
    runningPanel.style.position = "relative";
    runningPanel.style.top = `${Math.max(shift, 0) * -1}px`;
  });
  await expect.poll(async () =>
    page.evaluate(() => {
      const popup = document.querySelector("[data-testid='state-issues-review']");
      const runningPanel = document.querySelector("[data-testid='running-panel']");
      if (!(popup instanceof HTMLElement) || !(runningPanel instanceof HTMLElement)) {
        return "missing";
      }

      const popupRect = popup.getBoundingClientRect();
      const runningRect = runningPanel.getBoundingClientRect();
      const left = Math.max(popupRect.left, runningRect.left, 0);
      const top = Math.max(popupRect.top, runningRect.top, 0);
      const right = Math.min(popupRect.right, runningRect.right, window.innerWidth);
      const bottom = Math.min(popupRect.bottom, runningRect.bottom, window.innerHeight);
      if (left >= right || top >= bottom) {
        return "no-overlap";
      }

      const probeX = left + Math.min(16, (right - left) / 2);
      const probeY = top + Math.min(16, (bottom - top) / 2);
      const topElement = document.elementFromPoint(probeX, probeY);
      if (!(topElement instanceof HTMLElement)) {
        return "no-top-element";
      }

      return popup.contains(topElement) ? "popup-on-top" : "running-panel-on-top";
    }),
  ).toBe("popup-on-top");
  await expect(page.getByTestId("state-issue-review-COLIN-24").locator(".state-issue-id-link")).toHaveAttribute(
    "href",
    "https://linear.app/example/issue/COLIN-24",
  );
  await expect(page.getByTestId("state-issue-review-COLIN-24").locator(".state-issue-title-link")).toHaveAttribute(
    "href",
    "/linear/issues/issue-demo-5/metadata",
  );
  await expect(page.getByTestId("worker-card-COLIN-7")).toBeVisible();
  await expect(page.getByTestId("context-window-COLIN-7")).toHaveText("Context window: 70% left (78.4K used / 258K)");
  await expect(page.getByTestId("context-window-bar-COLIN-7")).toHaveAttribute("aria-valuenow", "30");
  await expect(page.getByTestId("rate-limits-linear-linear_requests")).toBeVisible();
  const linearRateLimitRow = page.getByTestId("rate-limits-linear-linear_requests");
  await expect(linearRateLimitRow.locator("[role='progressbar']")).toHaveAttribute("aria-valuenow", "75");
  await expect(linearRateLimitRow).not.toContainText("25 of 100 remaining");
  await expect(linearRateLimitRow.locator(".rate-limit-progress-detail")).toHaveText("next request in 3s");
  const linearRateLimitLayout = await linearRateLimitRow.evaluate((row) => {
    const bar = row.querySelector(".rate-limit-progress-bar");
    const detail = row.querySelector(".rate-limit-progress-detail");
    if (!(bar instanceof HTMLElement) || !(detail instanceof HTMLElement)) {
      throw new Error("missing linear rate limit detail");
    }
    return {
      barBottom: bar.getBoundingClientRect().bottom,
      detailTop: detail.getBoundingClientRect().top,
    };
  });
  expect(linearRateLimitLayout.detailTop).toBeGreaterThanOrEqual(linearRateLimitLayout.barBottom);
  await expect(page.locator(".stats")).toContainText("Runtime");
  await expect(page.locator(".stats")).toContainText("7m");
  await expect(page.locator(".stats")).toContainText("5,000");
  await expect(page.getByText("API snapshot")).toHaveCount(0);
  await expect(page.getByTestId("shell-instance")).toHaveCount(0);

  await page.getByTestId("state-issue-review-COLIN-24").locator(".state-issue-title-link").click();
  await expect(page).toHaveURL(/\/linear\/issues\/issue-demo-5\/metadata$/);
  await expect(page.getByTestId("issue-metadata-panel")).toBeVisible();
});

test("dashboard refreshes from SSE and pause or resume preserves expanded state", async ({ page }) => {
  await page.goto("/");
  await page.getByTestId("refresh-button").click();
  await expect(page.getByTestId("refresh-button")).toHaveAttribute("aria-label", "Resume automatic refresh");

  const before = await page.getByTestId("turn-count-COLIN-7").textContent();
  const details = page.locator("#worker-output-details-COLIN-7");
  const stateIssuesDetails = page.locator("#state-issues-details-in-progress");
  const outputPre = page.getByTestId("worker-output-COLIN-7").locator("pre").first();
  const outputTime = page.getByTestId("worker-output-COLIN-7").locator("[data-local-time]").first();

  await details.evaluate((node: HTMLDetailsElement) => {
    node.open = true;
  });
  await expect(details).toHaveJSProperty("open", true);
  await stateIssuesDetails.evaluate((node: HTMLDetailsElement) => {
    node.open = true;
  });
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
  await expect(page.getByTestId("refresh-button")).toHaveAttribute("aria-label", "Pause automatic refresh");
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

test("dashboard marks the view stale when the SSE stream fails and recovers after reconnect", async ({ page }) => {
  await page.goto("/");

  const refreshStatus = page.getByTestId("refresh-status");
  await expect(refreshStatus).toHaveAttribute("data-refresh-status", "live");

  await page.getByTestId("refresh-button").click();
  await expect(page.getByTestId("refresh-button")).toHaveAttribute("aria-label", "Resume automatic refresh");

  let failNextStream = true;
  const routeHandler = async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (failNextStream && path === "/api/v1/events") {
      failNextStream = false;
      await route.abort("failed");
      return;
    }
    await route.continue();
  };
  await page.route("**/api/v1/events", routeHandler);

  await page.getByTestId("refresh-button").click();
  await expect(refreshStatus).not.toHaveAttribute("data-refresh-status", "stale");
  await expect(refreshStatus).toHaveAttribute("data-refresh-status", "stale");
  await expect(refreshStatus).toHaveText("Stale data");
  await expect(page.getByTestId("worker-card-COLIN-7")).toBeVisible();

  await page.unroute("**/api/v1/events", routeHandler);
  await page.getByTestId("refresh-button").click();
  await expect(page.getByTestId("refresh-button")).toHaveAttribute("aria-label", "Resume automatic refresh");
  await page.getByTestId("refresh-button").click();
  await expect(refreshStatus).toHaveAttribute("data-refresh-status", "live");
  await expect(refreshStatus).toHaveText("Live data");
});

test("issue metadata page reloads when the live stream reports a new snapshot", async ({ page }) => {
  await page.goto("/linear/issues/issue-demo-1/metadata");

  const renderedAt = page.getByTestId("metadata-rendered-at");
  const before = await renderedAt.textContent();
  await expect(renderedAt).not.toHaveText(before ?? "");
  await expect(page.getByTestId("issue-metadata-panel")).toBeVisible();
  await expect(page.getByTestId("issue-metadata-output")).toContainText("session_started");
});
