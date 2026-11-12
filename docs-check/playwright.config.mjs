import { defineConfig } from '@playwright/test'

const port = Number(process.env.PORT || 8765)

// Layout does not change with the theme, so the full suite runs in light, the default,
// and the dark project reruns only the checks a theme can break (tagged @themed).
export default defineConfig({
  testDir: '.',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? 'github' : 'list',
  use: { baseURL: `http://127.0.0.1:${port}` },
  projects: [
    { name: 'light', use: { colorScheme: 'light' } },
    { name: 'dark', use: { colorScheme: 'dark' }, grep: /@themed/ },
  ],
  webServer: {
    command: 'node serve.mjs',
    url: `http://127.0.0.1:${port}/index.html`,
    reuseExistingServer: !process.env.CI,
    env: { PORT: String(port) },
  },
})
