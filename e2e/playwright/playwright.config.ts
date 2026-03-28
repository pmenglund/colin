import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests",
  fullyParallel: true,
  retries: process.env.CI ? 2 : 0,
  timeout: 30_000,
  use: {
    baseURL: process.env.E2E_BASE_URL || "http://127.0.0.1:8080",
    trace: "on-first-retry",
  },
  webServer: {
    command: "go run ./testserver",
    url: process.env.E2E_BASE_URL || "http://127.0.0.1:8080",
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
