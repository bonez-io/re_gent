// Onboarding-specific routes from RFC 0005 Appendix A ("API contract shared by both
// compositions") that are not part of the general app surface: the self-hosted admin
// step, setup codes, the connections feed, sign-in method settings, and invitations.
// `GET /api/v1/auth/me` and its `AuthMeResponse`/`AuthOrg` shape already live in
// ../api/client and ../api/types — reused here rather than duplicated.
import { request } from './client'
import type { OnboardingState } from './types'

export type JoinPolicy = 'invite_only' | 'open'
export type DefaultRole = 'reader' | 'writer'

export type Org = {
  slug: string
  display_name: string
  server_url?: string
  join_policy?: JoinPolicy
  default_role?: DefaultRole
  onboarding?: OnboardingState
  allowed_github_orgs?: string[]
}

export type AdminOnboardingRequest = {
  org: { display_name: string; slug: string; server_url: string; join_policy: JoinPolicy; default_role: DefaultRole }
  admin: { username: string; display_name: string; email?: string; new_password: string }
}
export type AdminOnboardingUser = { id: string; username: string; display_name: string; email?: string }
export type AdminOnboardingResponse = { user: AdminOnboardingUser; csrf: string; org: Org }

export type SetupCodeResponse = { code: string; expires_at: string; command: string }
export type Connection = { project_id: string; display_name: string; remote: string; machine_name: string; connected_by: string; connected_at: string }
export type ConnectionsResponse = { connections: Connection[]; cursor: string }

export type AuthMethodSettings = {
  password: { enabled: boolean }
  github: { enabled: boolean; client_id?: string; base_url?: string; has_secret?: boolean; callback_url?: string }
  google: { enabled: boolean; client_id?: string; has_secret?: boolean; callback_url?: string }
  smtp: { enabled: boolean; host?: string; port?: number; username?: string; from?: string; has_password?: boolean }
}
// A missing secret/password field on PUT keeps the stored one — so the patch only ever
// carries a `client_secret`/`password` key when the admin actually typed something.
export type AuthMethodSettingsPatch = {
  password?: { enabled?: boolean; password?: string }
  github?: { enabled?: boolean; client_id?: string; base_url?: string; client_secret?: string }
  google?: { enabled?: boolean; client_id?: string; client_secret?: string }
  smtp?: { enabled?: boolean; host?: string; port?: number; username?: string; from?: string; password?: string }
}

export type InvitationOrgRole = 'admin' | 'member'
export type InvitationStatus = 'pending' | 'accepted' | 'expired' | 'revoked'
export type InvitationGrant = { project_id: string; role: string }
export type Invitation = {
  id: string
  email?: string
  username?: string
  org_role: InvitationOrgRole
  status: InvitationStatus
  expires_at: string
  // Present only on the response to the creation call, per Appendix A; a listed row may
  // not carry it back, so callers must treat it as optional and not assume a copy button
  // always has something to copy.
  link?: string
  emailed?: boolean
}
export type CreateInvitationRequest = { email?: string; username?: string; org_role: InvitationOrgRole; grants: InvitationGrant[] }
export type CreateInvitationResponse = { id: string; link: string; expires_at: string; emailed: boolean }

const orgPath = (slug: string) => `/api/v1/orgs/${encodeURIComponent(slug)}`

export const onboardingApi = {
  // Requires a session already obtained with the initial password (the sign-in screen
  // outside this wizard's scope), so no `current_password` field is sent here.
  submitAdmin: (body: AdminOnboardingRequest) =>
    request<AdminOnboardingResponse>('/api/v1/onboarding/admin', { method: 'POST', body: JSON.stringify(body) }),
  org: (slug: string) => request<Org>(orgPath(slug)),
  advance: (slug: string, state: OnboardingState) =>
    request<Org>(`${orgPath(slug)}/onboarding`, { method: 'POST', body: JSON.stringify({ state }) }),
  createSetupCode: (slug: string) =>
    request<SetupCodeResponse>(`${orgPath(slug)}/setup-codes`, { method: 'POST', body: JSON.stringify({}) }),
  // No `cursor` returns the current snapshot immediately; a `cursor` from a prior
  // response holds the request open up to 25s for a new row, per Appendix A.
  connections: (slug: string, cursor?: string, signal?: AbortSignal) =>
    request<ConnectionsResponse>(`${orgPath(slug)}/connections${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`, { signal }),
  authMethods: (slug: string) => request<AuthMethodSettings>(`${orgPath(slug)}/auth-methods`),
  putAuthMethods: (slug: string, body: AuthMethodSettingsPatch) =>
    request<undefined>(`${orgPath(slug)}/auth-methods`, { method: 'PUT', body: JSON.stringify(body) }),
  // Self-hosted answers a bare array, managed wraps it; accept both.
  invitations: async (slug: string) => {
    const response = await request<Invitation[] | { invitations: Invitation[] }>(`${orgPath(slug)}/invitations`)
    return Array.isArray(response) ? response : response.invitations
  },
  createInvitation: (slug: string, body: CreateInvitationRequest) =>
    request<CreateInvitationResponse>(`${orgPath(slug)}/invitations`, { method: 'POST', body: JSON.stringify(body) }),
  revokeInvitation: (slug: string, id: string) =>
    request<undefined>(`${orgPath(slug)}/invitations/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}
