// Access to the Wails-generated bindings.
//
// Wails writes frontend/wailsjs/go/api/API.js at build time. That directory is a
// build artefact and is gitignored, so importing it directly would break both
// `npm run typecheck` on a clean checkout and any future browser-based test
// harness. Reading the methods off `window.go` at call time — the same object
// the generated module wraps — keeps the source tree self-contained.

import type {
  ChatReply,
  Dashboard,
  Diff,
  Edit,
  Fleet,
  HyctlStatus,
  InstallResult,
  MCPPanel,
  MCPSyncResult,
  HeadPanel,
  ModelRegistry,
  ReviewOutcome,
  QuestionQueue,
  SecurityReport,
  Session,
  UpdateStatus,
  UpgradeResult,
  Version,
} from './types'

interface WailsGo {
  api: {
    API: {
      GetDashboard(): Promise<Dashboard>
      GetFleet(): Promise<Fleet>
      GetSession(runID: string): Promise<Session>
      GetEdits(runID: string): Promise<Edit[]>
      Chat(prompt: string, enumKey: string, runID: string, tier: string): Promise<ChatReply>
      ApproveEdit(file: string): Promise<ReviewOutcome>
      RejectEdit(file: string): Promise<ReviewOutcome>
      GetMCPServers(): Promise<MCPPanel>
      SyncMCPRegistry(): Promise<MCPSyncResult>
      GetHeads(): Promise<HeadPanel>
      GetPendingQuestions(): Promise<QuestionQueue>
      AnswerQuestion(taskID: string, answer: string): Promise<ChatReply>
      DeclineQuestion(taskID: string, reason: string): Promise<void>
      ChatEnums(): Promise<string[]>
      NewRunID(): Promise<string>
      GetDiff(runID: string, ref: string, file: string): Promise<Diff>
      GetVersion(): Promise<Version>
      GetUpdateStatus(): Promise<UpdateStatus>
      TriggerUpgrade(): Promise<UpgradeResult>
      CheckHyctl(): Promise<HyctlStatus>
      InstallHyctl(): Promise<InstallResult>
      GetSecurity(): Promise<SecurityReport>
      GetModels(): Promise<ModelRegistry>
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
export const GetEdits = (runID: string): Promise<Edit[]> => backend().GetEdits(runID)
export const GetDiff = (runID: string, ref: string, file: string): Promise<Diff> =>
  backend().GetDiff(runID, ref, file)
export const Chat = (
  prompt: string,
  enumKey: string,
  runID: string,
  tier: string,
): Promise<ChatReply> => backend().Chat(prompt, enumKey, runID, tier)

export const ApproveEdit = (file: string): Promise<ReviewOutcome> => backend().ApproveEdit(file)

export const RejectEdit = (file: string): Promise<ReviewOutcome> => backend().RejectEdit(file)

export const GetMCPServers = (): Promise<MCPPanel> => backend().GetMCPServers()

export const SyncMCPRegistry = (): Promise<MCPSyncResult> => backend().SyncMCPRegistry()
export const GetHeads = (): Promise<HeadPanel> => backend().GetHeads()

export const GetPendingQuestions = (): Promise<QuestionQueue> => backend().GetPendingQuestions()

export const AnswerQuestion = (taskID: string, answer: string): Promise<ChatReply> =>
  backend().AnswerQuestion(taskID, answer)

export const DeclineQuestion = (taskID: string, reason: string): Promise<void> =>
  backend().DeclineQuestion(taskID, reason)
export const ChatEnums = (): Promise<string[]> => backend().ChatEnums()
export const NewRunID = (): Promise<string> => backend().NewRunID()
export const GetVersion = (): Promise<Version> => backend().GetVersion()
export const GetUpdateStatus = (): Promise<UpdateStatus> => backend().GetUpdateStatus()
export const TriggerUpgrade = (): Promise<UpgradeResult> => backend().TriggerUpgrade()
export const CheckHyctl = (): Promise<HyctlStatus> => backend().CheckHyctl()
export const InstallHyctl = (): Promise<InstallResult> => backend().InstallHyctl()
export const GetSecurity = (): Promise<SecurityReport> => backend().GetSecurity()
export const GetModels = (): Promise<ModelRegistry> => backend().GetModels()
