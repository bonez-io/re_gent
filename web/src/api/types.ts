import type { FileChange, ToolCall } from '../components/ToolCallGroup'
import type { FileDiff } from '../components/FileDiffView'

export type RepoListResponse = { repos: string[] }
export type CreateRepoResponse = { repo_id: string; created: boolean }

/** GET /api/v1/projects — the id/display-name project picker source; falls back to /repos on 404. */
export type ProjectSummary = { id: string; display_name: string }
export type ProjectsResponse = { projects: ProjectSummary[] }

export type OnboardingState = 'admin_password' | 'connect' | 'users' | 'done'

export type CapabilitiesResponse = {
  deployment: 'self-hosted' | 'managed'
  api_version: string
  auth_methods: string[]
  auth_starts?: Record<string, string>
  /** Self-hosted only; absent once onboarding is done. */
  onboarding?: OnboardingState
  features: string[]
  /** Managed only. When true, GitHub and Google sign-in are provisioned and operated by
   *  re_gent itself — the org has nothing to configure. */
  identity_managed?: boolean
}

export type AccessUser = {
  id: string
  username: string
  display_name: string
  instance_owner: boolean
  created_at: string
}

export type ProjectRole = 'owner' | 'admin' | 'writer' | 'reader'
export type ProjectMember = AccessUser & { role: ProjectRole }

/** The RFC 0005 Appendix A shape returned by GET /api/v1/auth/me. */
export type AuthUser = { id: string; username?: string; display_name: string; email?: string }
export type AuthOrg = { slug: string; display_name: string; role: ProjectRole | string; onboarding?: OnboardingState }
export type AuthMeResponse = {
  user: AuthUser
  /** Legacy alias for `user`, kept for older servers and existing UI reads. */
  viewer?: AccessUser
  orgs: AuthOrg[]
  last_org?: string
  capabilities?: string[]
  auth_method?: string
  csrf_token?: string
}
export type AuthSessionResponse = { viewer: AccessUser; csrf_token: string }
export type PasswordLoginResponse = { user: AuthUser; csrf: string; password_change_required?: boolean }
export type CreateOrgResponse = AuthOrg
export type InvitationResponse = { org_display_name: string; email?: string; username?: string; methods: string[] }
export type AcceptInvitationResponse = { user: AuthUser; csrf: string; org: AuthOrg }
export type UsersResponse = { users: AccessUser[] }
export type MembersResponse = { members: ProjectMember[] }
export type CreateUserResponse = { user: AccessUser; initial_token: string }
export type PersonalAccessToken = { id: string; name: string; prefix: string; created_at: string; expires_at: string; last_used_at?: string }
export type TokensResponse = { tokens: PersonalAccessToken[] }
export type CreateTokenResponse = { token: PersonalAccessToken; secret: string }

export type SessionSummary = {
  session_id: string
  agent_id: string
  step_count: number
  last_activity: string
  title: string
  author?: { name?: string; email?: string }
}

export type SessionsResponse = { total_sessions: number; sessions: SessionSummary[] }

export type LogMessage = { type: string; message: { role: string; content: string } }
export type LogCause = { tool: string; tool_use_id: string; args: unknown; result: unknown }
export type LogStep = {
  hash: string
  timestamp: string
  origin: string
  parent: string
  tool: string
  tool_use_id: string
  causes: LogCause[]
  files: string[]
  args: unknown
  result: unknown
  messages: LogMessage[]
  session_id?: string
  tree?: string
  events?: TranscriptEvent[]
  author?: { name?: string; email?: string }
  usage?: { input_tokens?: number; output_tokens?: number; cache_read_tokens?: number }
  effects?: unknown[]
}
export type LogResponse = { session_id: string; steps: LogStep[] }

export type StatusResponse = {
  status: 'ok' | 'degraded'
  service: { name?: string; api_version?: string; started_at?: string; server_url?: string } | string
  repository: { id?: string; object_count?: number; ref_count?: number; session_count?: number; last_activity?: string }
}

export type TranscriptEvent = { type: string; timestamp?: string; text?: string; tool_name?: string; tool_use_id?: string; input?: unknown; output?: unknown }
export type TranscriptStep = {
  hash: string; parent: string; secondary_parent?: string; tree: string; timestamp: string; origin: string
  session_id: string; turn_id: string; agent_id: string; author?: { name?: string; email?: string }
  usage?: { input_tokens?: number; output_tokens?: number; cache_read_tokens?: number }
  effects?: unknown[]; files: string[]; causes: LogCause[]; events: TranscriptEvent[]
}
export type TranscriptResponse = { session: SessionSummary; steps: TranscriptStep[] }

export type StepListResponse = { steps: LogStep[] }
/** GET /<repo>/api/diff?step=<hash> — the per-file diff a step introduced over its parent. */
export type StepDiffResponse = { step_hash: string; parent_hash: string; total_files: number; files: FileDiff[] }
export type FileSummary = { path: string; mode?: number; size?: number; blob_hash: string; blame_hash?: string }
export type FilesResponse = { step_hash: string; tree_hash: string; total_files: number; files: FileSummary[] }
export type BlameResponse = {
  step_hash: string
  path: string
  blob_hash: string
  lines: Array<{ number: number; content: string; step_hash?: string; origin?: string; timestamp?: string }>
}

/** GET /<repo>/api/feed — a long-pollable stream of newly captured steps, used by the
 *  onboarding tutorial to detect when a guided prompt has landed. */
export type FeedStep = { hash: string; session_id: string; origin: string; turn_id: string; timestamp: string; files: string[]; prompt: string }
export type FeedResponse = { cursor: string; steps: FeedStep[] }

export type Conversation = {
  id: string
  title: string
  author?: string
  agent?: string
  model?: string
  branch: string
  steps: number
  files: number
  relativeTime: string
  dateGroup: 'Today' | 'Yesterday' | 'Earlier'
  status?: 'capturing' | 'complete' | 'failed' | 'legacy'
}

export type TranscriptEntry =
  | { type: 'user'; id: string; at: string; content: string }
  | { type: 'assistant'; id: string; at: string; content: string }
  | { type: 'reasoning'; id: string; at: string; duration: number; lines: string[] }
  | { type: 'tools'; id: string; at: string; calls: ToolCall[]; files?: FileChange[]; stepHash?: string }
  | { type: 'code'; id: string; at: string; filename: string; language: string; code: string }
  | { type: 'step'; id: string; at: string; hash: string; tree: string; turn: string; tokens: number; files: number }
