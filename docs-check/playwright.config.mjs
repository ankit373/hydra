import { defineConfig } from '@playwright/test'

const port = Number(process.env.PORT || 8765)

// Dark is pinned on every page until the light theme ships (Phase 4); running both
// schemes now means that phase starts with the check already in place.
export default defineConfig({
  testDir: '.',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? 'github' : 'list',
  use: { baseURL: `http://127.0.0.1:${port}` },
  projects: [
    { name: 'dark', use: { colorScheme: 'dark' } },
    { name: 'light', use: { colorScheme: 'light' } },
  ],
  webServer: {
    command: 'node serve.mjs',
    url: `http://127.0.0.1:${port}/index.html`,
    reuseExistingServer: !process.env.CI,
    env: { PORT: String(port) },
  },
})
