import type { BlameResponse, CreateRepoResponse, FilesResponse, LogResponse, LogStep, RepoListResponse, SessionsResponse, StatusResponse, TranscriptResponse } from './types'
import {
  demoRepoId,
  mockBlameResponse,
  mockFilesResponse,
  mockLogResponse,
  mockSessionsResponse,
  mockStatusResponse,
  mockTranscriptResponse,
} from '../mocks/regent'

export class ApiError extends Error {
  status: number
  body?: unknown
  constructor(status: number, message: string, body?: unknown) { super(message); this.status = status; this.body = body }
}

export class OfflineError extends Error {
  constructor(message = 'Cannot reach the re_gent server') { super(message) }
}

const token = import.meta.env.VITE_REGENT_TOKEN as string | undefined

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response
  try {
    response = await fetch(path, {
      ...init,
      headers: { Accept: 'application/json', ...(init?.body ? { 'Content-Type': 'application/json' } : {}), ...(token ? { Authorization: `Bearer ${token}` } : {}), ...init?.headers },
    })
  } catch {
    throw new OfflineError()
  }
  const text = await response.text()
  let body: unknown
  try { body = text ? JSON.parse(text) : undefined } catch { body = text }
  if (!response.ok) {
    const message = typeof body === 'object' && body && 'error' in body ? String((body as { error: unknown }).error) : `${response.status} ${response.statusText}`
    throw new ApiError(response.status, message, body)
  }
  return body as T
}

const repoPath = (repoId: string) => `/${encodeURIComponent(repoId)}/api`
const demoOnly = (repoId: string) => repoId === demoRepoId
const showLocalDemoRepo = import.meta.env.DEV && !import.meta.env.STORYBOOK

export const api = {
  listRepos: async () => {
    const repos = await request<RepoListResponse>('/repos')
    return { repos: showLocalDemoRepo ? [demoRepoId, ...repos.repos.filter((repo) => repo !== demoRepoId)] : repos.repos }
  },
  createRepo: (repoId: string) => request<CreateRepoResponse>('/repos', { method: 'POST', body: JSON.stringify({ repo_id: repoId }) }),
  sessions: (repoId: string) => demoOnly(repoId) ? Promise.resolve(mockSessionsResponse) : request<SessionsResponse>(`${repoPath(repoId)}/sessions`),
  log: (repoId: string, sessionId: string) => demoOnly(repoId) ? Promise.resolve({ ...mockLogResponse, session_id: sessionId }) : request<LogResponse>(`${repoPath(repoId)}/log?session=${encodeURIComponent(sessionId)}&limit=500`),
  transcript: (repoId: string, sessionId: string) => demoOnly(repoId) ? Promise.resolve({ ...mockTranscriptResponse, session: mockSessionsResponse.sessions.find((session) => session.session_id === sessionId) || mockTranscriptResponse.session }) : request<TranscriptResponse>(`${repoPath(repoId)}/transcript?session=${encodeURIComponent(sessionId)}`),
  status: (repoId: string) => demoOnly(repoId) ? Promise.resolve(mockStatusResponse) : request<StatusResponse>(`${repoPath(repoId)}/status`),
  step: (repoId: string, hash: string) => request<LogStep>(`${repoPath(repoId)}/steps/${encodeURIComponent(hash)}`),
  files: (repoId: string, scope: { step?: string; session?: string } = {}) => {
    if (demoOnly(repoId)) return Promise.resolve(mockFilesResponse)
    const query = new URLSearchParams()
    if (scope.step) query.set('step', scope.step)
    else if (scope.session) query.set('session', scope.session)
    return request<FilesResponse>(`${repoPath(repoId)}/files${query.size ? `?${query}` : ''}`)
  },
  blame: (repoId: string, step: string, path: string) => demoOnly(repoId) ? Promise.resolve({ ...mockBlameResponse, step_hash: step, path }) : request<BlameResponse>(`${repoPath(repoId)}/blame?step=${encodeURIComponent(step)}&path=${encodeURIComponent(path)}`),
}
