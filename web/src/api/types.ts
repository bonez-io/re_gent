import type { FileChange, ToolCall } from '../components/ToolCallGroup'

export type RepoListResponse = { repos: string[] }
export type CreateRepoResponse = { repo_id: string; created: boolean }

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
export type FileSummary = { path: string; mode?: number; size?: number; blob_hash: string; blame_hash?: string }
export type FilesResponse = { step_hash: string; tree_hash: string; total_files: number; files: FileSummary[] }
export type BlameResponse = {
  step_hash: string
  path: string
  blob_hash: string
  lines: Array<{ number: number; content: string; step_hash?: string; origin?: string; timestamp?: string }>
}

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
  | { type: 'tools'; id: string; at: string; calls: ToolCall[]; files?: FileChange[] }
  | { type: 'code'; id: string; at: string; filename: string; language: string; code: string }
  | { type: 'step'; id: string; at: string; hash: string; tree: string; turn: string; tokens: number; files: number }
