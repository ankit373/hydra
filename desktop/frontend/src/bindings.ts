// Access to the Wails-generated bindings.
//
// Wails writes frontend/wailsjs/go/api/API.js at build time. That directory is a
// build artefact and is gitignored, so importing it directly would break both
// `npm run typecheck` on a clean checkout and any future browser-based test
// harness. Reading the methods off `window.go` at call time — the same object
// the generated module wraps — keeps the source tree self-contained.

import type { Dashboard, Fleet, Session, Version } from './types'

interface WailsGo {
  api: {
    API: {
      GetDashboard(): Promise<Dashboard>
      GetFleet(): Promise<Fleet>
      GetSession(runID: string): Promise<Session>
      GetVersion(): Promise<Version>
    }
  }
}

declare global {
  interface Window {
    go?: WailsGo
  }
}

function backend() {
  const go = window.go
  if (!go?.api?.API) {
    throw new Error('Wails backend not available — run `wails dev` rather than `vite` alone')
  }
  return go.api.API
}

export const GetDashboard = (): Promise<Dashboard> => backend().GetDashboard()
export const GetFleet = (): Promise<Fleet> => backend().GetFleet()
export const GetSession = (runID: string): Promise<Session> => backend().GetSession(runID)
export const GetVersion = (): Promise<Version> => backend().GetVersion()
