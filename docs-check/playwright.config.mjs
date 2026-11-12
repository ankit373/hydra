import { defineConfig } from '@playwright/test'

const port = Number(process.env.PORT || 8765)

// Every page pins data-theme="dark" until the light theme ships (Phase 4), so a
// light project would rerun identical pages; Phase 4 adds it back.
export default defineConfig({
  testDir: '.',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? 'github' : 'list',
  use: { baseURL: `http://127.0.0.1:${port}` },
  projects: [{ name: 'dark', use: { colorScheme: 'dark' } }],
  webServer: {
    command: 'node serve.mjs',
    url: `http://127.0.0.1:${port}/index.html`,
    reuseExistingServer: !process.env.CI,
    env: { PORT: String(port) },
  },
})
