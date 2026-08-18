import type { BlameResponse, CreateRepoResponse, FilesResponse, LogResponse, LogStep, RepoListResponse, SessionsResponse, StatusResponse, TranscriptResponse } from './types'

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

export const api = {
  listRepos: () => request<RepoListResponse>('/repos'),
  createRepo: (repoId: string) => request<CreateRepoResponse>('/repos', { method: 'POST', body: JSON.stringify({ repo_id: repoId }) }),
  sessions: (repoId: string) => request<SessionsResponse>(`${repoPath(repoId)}/sessions`),
  log: (repoId: string, sessionId: string) => request<LogResponse>(`${repoPath(repoId)}/log?session=${encodeURIComponent(sessionId)}&limit=500`),
  transcript: (repoId: string, sessionId: string) => request<TranscriptResponse>(`${repoPath(repoId)}/transcript?session=${encodeURIComponent(sessionId)}`),
  status: (repoId: string) => request<StatusResponse>(`${repoPath(repoId)}/status`),
  step: (repoId: string, hash: string) => request<LogStep>(`${repoPath(repoId)}/steps/${encodeURIComponent(hash)}`),
  files: (repoId: string, scope: { step?: string; session?: string } = {}) => {
    const query = new URLSearchParams()
    if (scope.step) query.set('step', scope.step)
    else if (scope.session) query.set('session', scope.session)
    return request<FilesResponse>(`${repoPath(repoId)}/files${query.size ? `?${query}` : ''}`)
  },
  blame: (repoId: string, step: string, path: string) => request<BlameResponse>(`${repoPath(repoId)}/blame?step=${encodeURIComponent(step)}&path=${encodeURIComponent(path)}`),
}
