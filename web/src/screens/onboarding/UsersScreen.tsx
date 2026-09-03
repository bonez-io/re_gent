import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { onboardingApi, type AuthMethodSettings, type AuthMethodSettingsPatch, type Invitation, type InvitationOrgRole } from '../../api/onboarding'
import { OnboardingLayout, OnboardingPending, OnboardingProblem } from './chrome'
import { onboardingPathFor } from './path'
import { useOnboardingBase } from './shared'

const statusTint: Record<Invitation['status'], string> = { pending: 'text-yellow', accepted: 'text-green', expired: 'text-ink-3', revoked: 'text-red' }

type AuthDraft = {
  githubEnabled: boolean; githubClientId: string; githubBaseUrl: string; githubSecret: string
  googleEnabled: boolean; googleClientId: string; googleSecret: string
  smtpEnabled: boolean; smtpHost: string; smtpPort: string; smtpUsername: string; smtpFrom: string; smtpPassword: string
}
const emptyDraft: AuthDraft = { githubEnabled: false, githubClientId: '', githubBaseUrl: '', githubSecret: '', googleEnabled: false, googleClientId: '', googleSecret: '', smtpEnabled: false, smtpHost: '', smtpPort: '', smtpUsername: '', smtpFrom: '', smtpPassword: '' }
const draftFrom = (settings: AuthMethodSettings): AuthDraft => ({
  githubEnabled: settings.github.enabled, githubClientId: settings.github.client_id ?? '', githubBaseUrl: settings.github.base_url ?? '', githubSecret: '',
  googleEnabled: settings.google.enabled, googleClientId: settings.google.client_id ?? '', googleSecret: '',
  smtpEnabled: settings.smtp.enabled, smtpHost: settings.smtp.host ?? '', smtpPort: settings.smtp.port ? String(settings.smtp.port) : '', smtpUsername: settings.smtp.username ?? '', smtpFrom: settings.smtp.from ?? '', smtpPassword: '',
})

/** Screen 3: sign-in methods (self-hosted only) and invitations (both compositions). */
export function UsersScreen() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { deployment, slug, org } = useOnboardingBase()

  const authMethods = useQuery({ queryKey: ['onboarding-auth-methods', slug], queryFn: () => onboardingApi.authMethods(slug!), enabled: Boolean(slug) && deployment === 'self-hosted', retry: false })
  const connections = useQuery({ queryKey: ['onboarding-connections-snapshot', slug], queryFn: () => onboardingApi.connections(slug!), enabled: Boolean(slug), retry: false })
  const invitations = useQuery({ queryKey: ['onboarding-invitations', slug], queryFn: () => onboardingApi.invitations(slug!), enabled: Boolean(slug), retry: false })

  const [draft, setDraft] = useState<AuthDraft>(emptyDraft)
  const draftInitialized = useRef(false)
  useEffect(() => {
    if (authMethods.data && !draftInitialized.current) { setDraft(draftFrom(authMethods.data)); draftInitialized.current = true }
  }, [authMethods.data])

  const saveAuthMethods = useMutation({
    mutationFn: () => {
      const patch: AuthMethodSettingsPatch = {
        github: { enabled: draft.githubEnabled, client_id: draft.githubClientId, base_url: draft.githubBaseUrl, ...(draft.githubSecret ? { client_secret: draft.githubSecret } : {}) },
        google: { enabled: draft.googleEnabled, client_id: draft.googleClientId, ...(draft.googleSecret ? { client_secret: draft.googleSecret } : {}) },
        smtp: { enabled: draft.smtpEnabled, host: draft.smtpHost, port: draft.smtpPort ? Number(draft.smtpPort) : undefined, username: draft.smtpUsername, from: draft.smtpFrom, ...(draft.smtpPassword ? { password: draft.smtpPassword } : {}) },
      }
      return onboardingApi.putAuthMethods(slug!, patch)
    },
    onSuccess: () => { setDraft((prev) => ({ ...prev, githubSecret: '', googleSecret: '', smtpPassword: '' })); void queryClient.invalidateQueries({ queryKey: ['onboarding-auth-methods', slug] }) },
  })

  const defaultRole = org.data?.default_role ?? 'reader'
  const defaultGrants = useMemo(() => (connections.data?.connections ?? []).map((connection) => ({ project_id: connection.project_id, role: defaultRole })), [connections.data, defaultRole])

  const [inviteBy, setInviteBy] = useState<'email' | 'username'>('email')
  const [inviteValue, setInviteValue] = useState('')
  const [inviteRole, setInviteRole] = useState<InvitationOrgRole>('member')
  const [lastInvite, setLastInvite] = useState<{ id: string; link: string }>()
  const [copiedId, setCopiedId] = useState<string>()

  const createInvitation = useMutation({
    mutationFn: () => onboardingApi.createInvitation(slug!, { ...(inviteBy === 'email' ? { email: inviteValue } : { username: inviteValue }), org_role: inviteRole, grants: defaultGrants }),
    onSuccess: async (response) => {
      setLastInvite({ id: response.id, link: response.link })
      setInviteValue('')
      await queryClient.invalidateQueries({ queryKey: ['onboarding-invitations', slug] })
    },
  })
  const revokeInvitation = useMutation({
    mutationFn: (id: string) => onboardingApi.revokeInvitation(slug!, id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['onboarding-invitations', slug] }),
  })
  const advance = useMutation({
    mutationFn: () => onboardingApi.advance(slug!, 'done'),
    onSuccess: () => navigate(onboardingPathFor({ slug: slug!, onboarding: 'done' }, deployment)),
  })

  const copyLink = async (id: string, link: string) => {
    try { await navigator.clipboard.writeText(link); setCopiedId(id); setTimeout(() => setCopiedId((current) => current === id ? undefined : current), 1500) } catch { /* clipboard denied — the link stays selectable in the row */ }
  }

  if (!slug || org.isPending) return <OnboardingPending label="Loading organization…" />
  if (org.error) return <OnboardingProblem error={org.error} onRetry={() => org.refetch()} />

  return <OnboardingLayout deployment={deployment} current="users" wide title="Users" description="Choose how people sign in, then invite your team.">
    <section>
      <h2 className="m-0 text-[12.5px] font-semibold">Sign-in methods</h2>
      {deployment === 'managed'
        ? <div className="mt-2 grid gap-1.5 rounded-[8px] border border-line bg-inset p-3 text-[11.5px]">
          <div className="flex items-center justify-between"><span>GitHub</span><span className="text-ink-3">On · not configurable</span></div>
          <div className="flex items-center justify-between"><span>Google</span><span className="text-ink-3">On · not configurable</span></div>
          <p className="m-0 mt-1 text-[10.5px] text-ink-3">The service manages these OAuth apps. Verified domains come later.</p>
        </div>
        : authMethods.isPending ? <p className="mt-2 text-[11.5px] text-ink-3">Loading sign-in settings…</p>
          : authMethods.error ? <p role="alert" className="mt-2 text-[11.5px] text-red">{authMethods.error.message}</p>
            : <div className="mt-2 grid gap-3">
              <div className="flex items-center justify-between rounded-[8px] border border-line bg-inset px-3 py-2 text-[11.5px]"><span>Password</span><span className="text-ink-3">On · cannot be turned off in beta</span></div>
              <div className="flex items-center justify-between rounded-[8px] border border-line bg-inset px-3 py-2 text-[11.5px]"><span>Invitation links</span><span className="text-ink-3">On · email delivery optional via SMTP below</span></div>

              <div className="rounded-[8px] border border-line p-3">
                <label className="flex items-center gap-2 text-[11.5px] font-medium"><input type="checkbox" checked={draft.githubEnabled} onChange={(event) => setDraft((prev) => ({ ...prev, githubEnabled: event.target.checked }))} />GitHub</label>
                {draft.githubEnabled && <div className="mt-2 grid grid-cols-2 gap-2 max-sm:grid-cols-1">
                  <input placeholder="Client ID" value={draft.githubClientId} onChange={(event) => setDraft((prev) => ({ ...prev, githubClientId: event.target.value }))} className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
                  <input placeholder={authMethods.data?.github.has_secret ? 'Client secret (set — leave blank to keep)' : 'Client secret'} type="password" value={draft.githubSecret} onChange={(event) => setDraft((prev) => ({ ...prev, githubSecret: event.target.value }))} className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
                  <input placeholder="Base URL (GitHub Enterprise Server, optional)" value={draft.githubBaseUrl} onChange={(event) => setDraft((prev) => ({ ...prev, githubBaseUrl: event.target.value }))} className="col-span-2 h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent max-sm:col-span-1" />
                  {authMethods.data?.github.callback_url && <p className="col-span-2 m-0 text-[10.5px] text-ink-3 max-sm:col-span-1">Callback URL to paste into GitHub: <code className="text-ink-2">{authMethods.data.github.callback_url}</code></p>}
                </div>}
              </div>

              <div className="rounded-[8px] border border-line p-3">
                <label className="flex items-center gap-2 text-[11.5px] font-medium"><input type="checkbox" checked={draft.googleEnabled} onChange={(event) => setDraft((prev) => ({ ...prev, googleEnabled: event.target.checked }))} />Google</label>
                {draft.googleEnabled && <div className="mt-2 grid grid-cols-2 gap-2 max-sm:grid-cols-1">
                  <input placeholder="Client ID" value={draft.googleClientId} onChange={(event) => setDraft((prev) => ({ ...prev, googleClientId: event.target.value }))} className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
                  <input placeholder={authMethods.data?.google.has_secret ? 'Client secret (set — leave blank to keep)' : 'Client secret'} type="password" value={draft.googleSecret} onChange={(event) => setDraft((prev) => ({ ...prev, googleSecret: event.target.value }))} className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
                  {authMethods.data?.google.callback_url && <p className="col-span-2 m-0 text-[10.5px] text-ink-3 max-sm:col-span-1">Callback URL: <code className="text-ink-2">{authMethods.data.google.callback_url}</code></p>}
                </div>}
              </div>

              <div className="rounded-[8px] border border-line p-3">
                <label className="flex items-center gap-2 text-[11.5px] font-medium"><input type="checkbox" checked={draft.smtpEnabled} onChange={(event) => setDraft((prev) => ({ ...prev, smtpEnabled: event.target.checked }))} />Email delivery (SMTP)</label>
                {draft.smtpEnabled && <div className="mt-2 grid grid-cols-2 gap-2 max-sm:grid-cols-1">
                  <input placeholder="Host" value={draft.smtpHost} onChange={(event) => setDraft((prev) => ({ ...prev, smtpHost: event.target.value }))} className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
                  <input placeholder="Port" inputMode="numeric" value={draft.smtpPort} onChange={(event) => setDraft((prev) => ({ ...prev, smtpPort: event.target.value }))} className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
                  <input placeholder="Username" value={draft.smtpUsername} onChange={(event) => setDraft((prev) => ({ ...prev, smtpUsername: event.target.value }))} className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
                  <input placeholder={authMethods.data?.smtp.has_password ? 'Password (set — leave blank to keep)' : 'Password'} type="password" value={draft.smtpPassword} onChange={(event) => setDraft((prev) => ({ ...prev, smtpPassword: event.target.value }))} className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
                  <input placeholder="From address" value={draft.smtpFrom} onChange={(event) => setDraft((prev) => ({ ...prev, smtpFrom: event.target.value }))} className="col-span-2 h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent max-sm:col-span-1" />
                </div>}
              </div>

              {saveAuthMethods.error && <p role="alert" className="m-0 text-[11px] text-red">{saveAuthMethods.error.message}</p>}
              <button type="button" disabled={saveAuthMethods.isPending} onClick={() => saveAuthMethods.mutate()} className="h-9 w-fit rounded-[4px] bg-field px-3 text-[11.5px] font-medium shadow-hairline hover:bg-hover-2 disabled:opacity-40">{saveAuthMethods.isPending ? 'Saving…' : 'Save sign-in methods'}</button>
            </div>}
    </section>

    <section className="mt-5">
      <h2 className="m-0 text-[12.5px] font-semibold">Invite your team</h2>
      <form className="mt-2 grid grid-cols-[100px_minmax(0,1fr)_120px_auto] gap-2 max-md:grid-cols-2 max-sm:grid-cols-1" onSubmit={(event) => { event.preventDefault(); createInvitation.mutate() }}>
        <select aria-label="Invite by" value={inviteBy} onChange={(event) => setInviteBy(event.target.value as 'email' | 'username')} className="native-select h-9"><option value="email">Email</option><option value="username">Username</option></select>
        <input required aria-label={inviteBy === 'email' ? 'Email' : 'Username'} type={inviteBy === 'email' ? 'email' : 'text'} value={inviteValue} onChange={(event) => setInviteValue(event.target.value)} placeholder={inviteBy === 'email' ? 'teammate@example.com' : 'username'} className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
        <select aria-label="Organization role" value={inviteRole} onChange={(event) => setInviteRole(event.target.value as InvitationOrgRole)} className="native-select h-9"><option value="member">Member</option><option value="admin">Admin</option></select>
        <button type="submit" disabled={createInvitation.isPending || !inviteValue} className="h-9 rounded-[4px] bg-accent px-3 text-[11.5px] font-medium text-page disabled:opacity-40">{createInvitation.isPending ? 'Inviting…' : 'Invite'}</button>
      </form>
      {createInvitation.error && <p role="alert" className="mt-2 text-[11px] text-red">{createInvitation.error.message}</p>}

      {lastInvite && <div role="status" className="mt-3 flex items-center gap-2 overflow-hidden rounded-[8px] border border-green/30 bg-green/10 px-3 py-2">
        <span className="min-w-0 flex-1 truncate font-mono text-[11px]">{lastInvite.link}</span>
        <button type="button" onClick={() => void copyLink(lastInvite.id, lastInvite.link)} className="shrink-0 rounded-[4px] bg-field px-2.5 py-1 text-[10.5px] shadow-hairline hover:bg-hover-2">{copiedId === lastInvite.id ? 'Copied' : 'Copy link'}</button>
      </div>}

      <div className="mt-3 overflow-hidden rounded-[8px] border border-line">
        <div className="grid grid-cols-[minmax(0,1fr)_90px_90px_auto] gap-2 border-b border-line bg-inset px-3 py-1.5 text-[10px] font-medium uppercase tracking-[0.08em] text-ink-3 max-sm:grid-cols-[1fr_70px_auto] max-sm:[&>*:nth-child(2)]:hidden"><span>Invitee</span><span>Role</span><span>Status</span><span /></div>
        {invitations.isPending && <p className="m-0 px-3 py-3 text-[11.5px] text-ink-3">Loading invitations…</p>}
        {invitations.error && <p role="alert" className="m-0 px-3 py-3 text-[11.5px] text-red">{invitations.error.message}</p>}
        {invitations.data?.length === 0 && <p className="m-0 px-3 py-3 text-[11.5px] text-ink-3">No invitations yet.</p>}
        {invitations.data?.map((invitation) => <div key={invitation.id} className="grid grid-cols-[minmax(0,1fr)_90px_90px_auto] items-center gap-2 border-b border-line px-3 py-2 text-[12px] last:border-0 max-sm:grid-cols-[1fr_70px_auto] max-sm:[&>*:nth-child(2)]:hidden">
          <span className="truncate">{invitation.email || invitation.username}</span>
          <span className="text-[11px] text-ink-3">{invitation.org_role}</span>
          <span className={`text-[11px] ${statusTint[invitation.status]}`}>{invitation.status}</span>
          <div className="flex shrink-0 items-center gap-1">
            {invitation.link && <button type="button" onClick={() => void copyLink(invitation.id, invitation.link!)} className="h-7 rounded-[4px] px-2 text-[10.5px] text-ink-3 hover:bg-hover hover:text-ink">{copiedId === invitation.id ? 'Copied' : 'Copy link'}</button>}
            {invitation.status === 'pending' && <button type="button" disabled={revokeInvitation.isPending} onClick={() => { if (window.confirm('Revoke this invitation?')) revokeInvitation.mutate(invitation.id) }} className="h-7 rounded-[4px] px-2 text-[10.5px] text-ink-3 hover:bg-hover hover:text-red disabled:opacity-40">Revoke</button>}
          </div>
        </div>)}
      </div>
    </section>

    {advance.error && <p role="alert" className="mt-3 text-[11px] text-red">{advance.error.message}</p>}
    <div className="mt-4 flex items-center justify-end">
      <button type="button" disabled={advance.isPending} onClick={() => advance.mutate()} className="h-9 rounded-[4px] bg-accent px-3 text-[11.5px] font-medium text-page disabled:opacity-50">{advance.isPending ? 'Continuing…' : 'Continue'}</button>
    </div>
  </OnboardingLayout>
}
