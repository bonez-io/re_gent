import type { AcceptInvitationResponse, AuthMeResponse, AuthSessionResponse, BlameResponse, CapabilitiesResponse, CreateOrgResponse, CreateRepoResponse, CreateTokenResponse, CreateUserResponse, FilesResponse, InvitationResponse, LogResponse, LogStep, MembersResponse, PasswordLoginResponse, ProjectRole, ProjectsResponse, RepoListResponse, SessionsResponse, StatusResponse, StepDiffResponse, TokensResponse, TranscriptResponse, UsersResponse } from './types'
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

let csrfToken = ''
export function currentCSRF() { return csrfToken }
export function rememberCSRFToken(token?: string) { if (token) csrfToken = token }

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response
  try {
    response = await fetch(path, {
      ...init,
      credentials: 'same-origin',
      headers: { Accept: 'application/json', ...(init?.body ? { 'Content-Type': 'application/json' } : {}), ...(init?.method && !['GET', 'HEAD', 'OPTIONS'].includes(init.method) && csrfToken ? { 'X-Regent-CSRF': csrfToken } : {}), ...init?.headers },
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

const rememberCSRF = <T extends { csrf_token?: string }>(response: T) => {
  if (response.csrf_token) csrfToken = response.csrf_token
  return response
}
// Password login and invitation acceptance return the token under `csrf`, not `csrf_token`.
const rememberCSRFField = <T extends { csrf?: string }>(response: T) => {
  rememberCSRFToken(response.csrf)
  return response
}

const repoPath = (repoId: string) => `/${encodeURIComponent(repoId)}/api`
const demoOnly = (repoId: string) => repoId === demoRepoId
// The demo workspace is a design fixture, not captured history, so it is opt-in
// (VITE_REGENT_DEMO=1). Injecting it into every dev build made the repo list
// claim a project the server does not host, and the picker could never reach
// the real ones.
const showLocalDemoRepo = import.meta.env.VITE_REGENT_DEMO === '1' && !import.meta.env.STORYBOOK

export const api = {
  capabilities: () => request<CapabilitiesResponse>('/api/v1/capabilities'),
  me: async () => rememberCSRF(await request<AuthMeResponse>('/api/v1/auth/me')),
  login: async (token: string) => rememberCSRF(await request<AuthSessionResponse>('/api/v1/auth/session', { method: 'POST', headers: { Authorization: `Bearer ${token}` } })),
  // Self-hosted only; rate limited. `password_change_required` stays true while the initial password is in force.
  passwordLogin: async (username: string, password: string) => rememberCSRFField(await request<PasswordLoginResponse>('/api/v1/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) })),
  logout: async () => { await request<undefined>('/api/v1/auth/session', { method: 'DELETE' }); csrfToken = '' },
  tokens: () => request<TokensResponse>('/api/v1/auth/tokens'),
  createToken: (name: string, expiresInDays: number) => request<CreateTokenResponse>('/api/v1/auth/tokens', { method: 'POST', body: JSON.stringify({ name, expires_in_days: expiresInDays }) }),
  revokeToken: (tokenId: string) => request<undefined>(`/api/v1/auth/tokens/${encodeURIComponent(tokenId)}`, { method: 'DELETE' }),
  users: () => request<UsersResponse>('/api/v1/users'),
  createUser: (username: string, displayName: string, repoId?: string, role?: ProjectRole) => request<CreateUserResponse>('/api/v1/users', { method: 'POST', body: JSON.stringify({ username, display_name: displayName, ...(repoId && role ? { repo_id: repoId, role } : {}) }) }),
  members: (repoId: string) => request<MembersResponse>(`/${encodeURIComponent(repoId)}/api/v1/access/members`),
  putMember: (repoId: string, userId: string, role: ProjectRole) => request<undefined>(`/${encodeURIComponent(repoId)}/api/v1/access/members`, { method: 'PUT', body: JSON.stringify({ user_id: userId, role }) }),
  deleteMember: (repoId: string, userId: string) => request<undefined>(`/${encodeURIComponent(repoId)}/api/v1/access/members/${encodeURIComponent(userId)}`, { method: 'DELETE' }),
  listRepos: async () => {
    const repos = await request<RepoListResponse>('/repos')
    return { repos: showLocalDemoRepo ? [demoRepoId, ...repos.repos.filter((repo) => repo !== demoRepoId)] : repos.repos }
  },
  createRepo: (repoId: string) => request<CreateRepoResponse>('/repos', { method: 'POST', body: JSON.stringify({ repo_id: repoId }) }),
  // Named projects with display names; older servers only expose /repos (bare ids), so
  // RepoHome falls back to listRepos when this route answers 404.
  listProjects: () => request<ProjectsResponse>('/api/v1/projects'),
  // Managed only; self-hosted answers 409 single_org since it always has exactly one.
  createOrg: (slug: string, displayName: string) => request<CreateOrgResponse>('/api/v1/orgs', { method: 'POST', body: JSON.stringify({ slug, display_name: displayName }) }),
  invitation: (token: string) => request<InvitationResponse>(`/api/v1/invitations/${encodeURIComponent(token)}`),
  acceptInvitation: async (token: string, body: { display_name: string; username?: string; password?: string }) => rememberCSRFField(await request<AcceptInvitationResponse>(`/api/v1/invitations/${encodeURIComponent(token)}/accept`, { method: 'POST', body: JSON.stringify(body) })),
  approveDevice: (userCode: string) => request<undefined>('/api/v1/auth/device/approve', { method: 'POST', body: JSON.stringify({ user_code: userCode, approve: true }) }),
  sessions: (repoId: string) => demoOnly(repoId) ? Promise.resolve(mockSessionsResponse) : request<SessionsResponse>(`${repoPath(repoId)}/sessions`),
  log: (repoId: string, sessionId: string) => demoOnly(repoId) ? Promise.resolve({ ...mockLogResponse, session_id: sessionId }) : request<LogResponse>(`${repoPath(repoId)}/log?session=${encodeURIComponent(sessionId)}&limit=500`),
  transcript: (repoId: string, sessionId: string) => demoOnly(repoId) ? Promise.resolve({ ...mockTranscriptResponse, session: mockSessionsResponse.sessions.find((session) => session.session_id === sessionId) || mockTranscriptResponse.session }) : request<TranscriptResponse>(`${repoPath(repoId)}/transcript?session=${encodeURIComponent(sessionId)}`),
  status: (repoId: string) => demoOnly(repoId) ? Promise.resolve(mockStatusResponse) : request<StatusResponse>(`${repoPath(repoId)}/status`),
  step: (repoId: string, hash: string) => request<LogStep>(`${repoPath(repoId)}/steps/${encodeURIComponent(hash)}`),
  // Fetched only when a diff is actually shown: a first step has no parent, so its diff is
  // the whole tree as additions and can run to hundreds of kilobytes.
  diff: (repoId: string, step: string) => request<StepDiffResponse>(`${repoPath(repoId)}/diff?step=${encodeURIComponent(step)}`),
  files: (repoId: string, scope: { step?: string; session?: string } = {}) => {
    if (demoOnly(repoId)) return Promise.resolve(mockFilesResponse)
    const query = new URLSearchParams()
    if (scope.step) query.set('step', scope.step)
    else if (scope.session) query.set('session', scope.session)
    return request<FilesResponse>(`${repoPath(repoId)}/files${query.size ? `?${query}` : ''}`)
  },
  blame: (repoId: string, step: string, path: string) => demoOnly(repoId) ? Promise.resolve({ ...mockBlameResponse, step_hash: step, path }) : request<BlameResponse>(`${repoPath(repoId)}/blame?step=${encodeURIComponent(step)}&path=${encodeURIComponent(path)}`),
}
