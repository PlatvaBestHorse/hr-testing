import { defineConfig, devices } from '@playwright/test'
export default defineConfig({
  testDir: './e2e',
  use: {
    baseURL: process.env.E2E_BASE_URL || 'http://127.0.0.1:8088',
    launchOptions: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
      ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
      : {},
  },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1366, height: 768 } } },
    { name: 'mobile', use: { ...devices['Desktop Chrome'], viewport: { width: 320, height: 568 }, isMobile: true } },
  ],
})
