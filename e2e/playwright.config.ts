import { defineConfig, devices } from '@playwright/test'

const PORT = 8099

// The harness starts a clean monitor binary (built at ../bin/monitor-server)
// via the webServer block, then drives it through Chromium. Screenshots, video,
// and a trace are captured for every test so the run can be reviewed offline
// (npm run report). Headless by default; `npm run test:headed` to watch live.
export default defineConfig({
  testDir: './tests',
  timeout: 45_000,
  expect: { timeout: 8_000 },
  fullyParallel: false,
  workers: 1,
  reporter: [['list'], ['html', { outputFolder: 'report', open: 'never' }]],
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    screenshot: 'on',
    video: 'on',
    trace: 'on',
  },
  globalSetup: './global-setup.ts',
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: 'cd .. && exec ./bin/monitor-server --config e2e/monitor.e2e.yaml',
    url: `http://127.0.0.1:${PORT}/healthz`,
    reuseExistingServer: false,
    timeout: 30_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
})
