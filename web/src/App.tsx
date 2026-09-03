import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom'
import { ApiError, OfflineError, api } from './api/client'
import { logToTranscript, transcriptToEntries } from './api/adapters'
import { languageForPath } from './lib/highlight'
import type { AuthMeResponse, CapabilitiesResponse, StatusResponse } from './api/types'
import { AgentIcon, agentColor, agentLabel } from './components/AgentIcon'
import { BlameView } from './components/BlameView'
import { ConversationTranscript } from './components/ConversationTranscript'
import { FileTree } from './components/FileTree'
import { ProjectSidebar, type RegentView, type SettingsView } from './components/ProjectSidebar'
import { ResizeHandle } from './components/ResizeHandle'
import { SessionSearch } from './components/SessionSearch'
import { TeamDashboard } from './components/TeamDashboard'
import { usePersistentPanelSize } from './lib/panelSize'
import { OnboardingRoutes, onboardingPathFor } from './screens/onboarding'
import { SettingsScreen, type SettingsSection } from './screens/SettingsScreen'
import { SkillsScreen } from './screens/SkillsScreen'

const defaultRepo = import.meta.env.VITE_REGENT_REPO_ID as string | undefined
const connectServerUrl = ((import.meta.env.VITE_REGENT_SERVER_URL as string | undefined) || (import.meta.env.PROD ? window.location.origin : 'http://127.0.0.1:7654')).replace(/\/+$/, '')
const connectCommand = `rgt connect ${connectServerUrl}`
const apiVersionOf = (data: StatusResponse) => typeof data.service === 'string' || !data.service.api_version ? undefined : `API v${data.service.api_version}`
// A stopped server rarely fails the fetch: the dev proxy and any production
// reverse proxy answer for it with a gateway status instead. Treating only a
// failed fetch as offline let the chrome report "connected" with nothing behind
// it, so gateway statuses count as unreachable too.
const isUnreachable = (error: unknown) => error instanceof OfflineError || (error instanceof ApiError && [502, 503, 504].includes(error.status))
const short = (value?: string) => value ? value.slice(0, 8) : '—'
const viewFor = (path: string): RegentView => path.includes('/settings') ? 'settings' : path.endsWith('/team') ? 'team' : path.endsWith('/files') ? 'files' : path.endsWith('/skills') ? 'skills' : 'sessions'
const settingsFor = (path: string): SettingsView => (path.match(/\/settings\/(general|status|users|data)/)?.[1] as SettingsView | undefined) ?? 'general'
const pathFor = (repoId: string, view: RegentView) => `/repos/${encodeURIComponent(repoId)}/${view === 'settings' ? 'settings/general' : view}`

function Pending({ label = 'Loading captured work…' }: { label?: string }) {
  return <div className="flex flex-1 items-center justify-center text-[12px] text-ink-3"><span className="mr-2 size-2 animate-pulse rounded-full bg-accent" />{label}</div>
}

function Problem({ error, onRetry }: { error: Error; onRetry?: () => void }) {
  const offline = isUnreachable(error)
  const missing = error instanceof ApiError && error.status === 404
  return <div className="m-auto max-w-sm px-6 py-10 text-center"><div className={`mx-auto mb-2 size-2 rounded-full ${offline ? 'bg-red' : 'bg-accent'}`} /><h2 className="m-0 text-[15px] font-semibold">{offline ? 'Server disconnected' : missing ? 'Data is not available yet' : 'Could not load this view'}</h2><p className="mt-1 text-[12px] leading-5 text-ink-3">{offline ? 'Start the local re_gent server on 127.0.0.1:7654, then retry.' : error.message}</p>{onRetry && <button onClick={onRetry} className="mt-3 h-8 rounded-[4px] bg-field px-3 text-[12px] shadow-hairline hover:bg-hover-2">Retry</button>}</div>
}

function Empty({ title, detail }: { title: string; detail: string }) {
  return <div className="m-auto max-w-md px-6 py-12 text-center"><img src="/favicon.svg" alt="" className="mx-auto mb-2 size-8 opacity-70" /><h2 className="m-0 text-[15px] font-semibold">{title}</h2><p className="mt-1 text-[12px] leading-5 text-ink-3">{detail}</p></div>
}

// Routes an anonymous visitor can always reach, regardless of sign-in state:
// an invitation link, and the two plain-text landing pages OAuth callbacks send people to.
const isPublicPath = (pathname: string) => pathname.startsWith('/invitations/') || pathname === '/not-invited' || pathname === '/sign-in-error'
// The onboarding wizard itself must render even while its own state is not yet "done" —
// otherwise AuthGate would redirect it back to itself in a loop.
const isSetupPath = (pathname: string) => pathname === '/setup' || pathname.startsWith('/setup/') || /^\/o\/[^/]+\/setup(\/|$)/.test(pathname)

function AuthGate({ children }: { children: React.ReactNode }) {
  const queryClient = useQueryClient()
  const location = useLocation()
  const capabilities = useQuery({ queryKey: ['capabilities'], queryFn: api.capabilities, retry: false })
  const secure = Boolean(capabilities.data)
  const legacyOpen = capabilities.error instanceof ApiError && [400, 404].includes(capabilities.error.status)
  const me = useQuery({ queryKey: ['auth-me'], queryFn: api.me, enabled: secure, retry: false })
  const refreshMe = () => queryClient.invalidateQueries({ queryKey: ['auth-me'] })

  if (isPublicPath(location.pathname)) return children

  if (capabilities.isPending) return <main className="flex min-h-screen bg-page text-ink"><Pending label="Reading server capabilities…" /></main>
  if (legacyOpen) return children
  if (capabilities.error) return <main className="flex min-h-screen bg-page text-ink"><Problem error={capabilities.error} onRetry={() => capabilities.refetch()} /></main>
  const caps = capabilities.data!

  if (me.isPending) return <main className="flex min-h-screen bg-page text-ink"><Pending label="Restoring your session…" /></main>
  if (me.error instanceof ApiError && [401, 403].includes(me.error.status)) {
    const inviteToken = new URLSearchParams(location.search).get('invite')
    if (inviteToken) return <InvitationScreen token={inviteToken} />
    return <SignInScreen capabilities={caps} onReady={refreshMe} />
  }
  if (me.error) return <main className="flex min-h-screen bg-page text-ink"><Problem error={me.error} onRetry={() => me.refetch()} /></main>
  const meData = me.data!
  const orgs = meData.orgs ?? []

  if (caps.deployment === 'managed' && orgs.length === 0) return <CreateOrgScreen onReady={refreshMe} />

  const onboardingState = caps.deployment === 'managed' ? orgs[0]?.onboarding : caps.onboarding
  if (onboardingState && onboardingState !== 'done' && !isSetupPath(location.pathname)) {
    const org = orgs[0]
    return <Navigate replace to={org ? onboardingPathFor(org, caps.deployment) : '/setup'} />
  }

  return children
}

// A relative provider-start URL (from capabilities.auth_starts) plus the query params it
// accepts: `return` (where the browser lands after the round trip) and `invite`, both signed
// into server-side state so they survive the redirect.
function withAuthParams(url: string, returnTo: string, invite?: string, extra?: Record<string, string>) {
  const params = new URLSearchParams(extra)
  params.set('return', returnTo)
  if (invite) params.set('invite', invite)
  return `${url}${url.includes('?') ? '&' : '?'}${params.toString()}`
}

function providerLabel(key: string) {
  if (key === 'github') return 'GitHub'
  if (key === 'google') return 'Google'
  return key.charAt(0).toUpperCase() + key.slice(1)
}

// Preserves the page the visitor was trying to reach (as `return`) and any invitation
// token already on the URL, stripped from the query string so it isn't duplicated.
function useReturnTarget() {
  const location = useLocation()
  const params = new URLSearchParams(location.search)
  const invite = params.get('invite') || undefined
  params.delete('invite')
  params.delete('return')
  const query = params.toString()
  return { returnTo: location.pathname + (query ? `?${query}` : ''), invite }
}

function SignInScreen({ capabilities, onReady }: { capabilities: CapabilitiesResponse; onReady: () => Promise<unknown> }) {
  const { returnTo, invite } = useReturnTarget()
  const navigate = useNavigate()
  const [devEmail, setDevEmail] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [token, setToken] = useState('')

  const passwordLogin = useMutation({
    mutationFn: () => api.passwordLogin(username, password),
    onSuccess: async (response) => {
      setPassword('')
      await onReady()
      // The initial admin password is still in force: send them straight into the wizard
      // that replaces it, rather than into a product screen guarded by a password we know
      // is about to be revoked.
      if (response.password_change_required) navigate(onboardingPathFor({ slug: '', onboarding: 'admin_password' }, capabilities.deployment))
    },
  })
  const tokenLogin = useMutation({ mutationFn: () => api.login(token), onSuccess: async () => { setToken(''); await onReady() } })

  const providerEntries = Object.entries(capabilities.auth_starts ?? {}).filter(([key]) => key !== 'dev')
  const devStart = capabilities.auth_starts?.dev
  const hasPassword = capabilities.auth_methods.includes('password')
  const legacyOnly = providerEntries.length === 0 && !devStart && !hasPassword

  return <main className="flex min-h-screen items-center justify-center bg-page p-4 text-ink"><section className="w-full max-w-sm overflow-hidden rounded-[8px] border border-line bg-canvas shadow-raised">
    <header className="border-b border-line px-5 py-4"><div className="flex items-center gap-2"><img src="/favicon.svg" alt="" className="size-7" /><h1 className="m-0 text-[16px] font-semibold">Sign in to re_gent</h1></div></header>
    <div className="grid gap-3 p-5">
      {providerEntries.map(([key, url]) => <a key={key} href={withAuthParams(url, returnTo, invite)} className="flex h-10 w-full items-center justify-center rounded-[4px] bg-field text-[12.5px] font-medium shadow-hairline hover:bg-hover-2">Continue with {providerLabel(key)}</a>)}

      {devStart && <form onSubmit={(event) => { event.preventDefault(); window.location.assign(withAuthParams(devStart, returnTo, invite, { email: devEmail })) }} className={`grid gap-1.5 ${providerEntries.length ? 'border-t border-line pt-3' : ''}`}>
        <label className="text-[11px] font-medium text-ink-2" htmlFor="dev-email">Dev sign-in</label>
        <div className="flex gap-1.5">
          <input id="dev-email" type="email" required placeholder="you@example.com" value={devEmail} onChange={(event) => setDevEmail(event.target.value)} className="h-9 min-w-0 flex-1 rounded-[4px] border-0 bg-field px-2.5 text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
          <button type="submit" className="h-9 shrink-0 rounded-[4px] bg-field px-3 text-[11.5px] font-medium shadow-hairline hover:bg-hover-2">Continue</button>
        </div>
      </form>}

      {hasPassword && <form onSubmit={(event) => { event.preventDefault(); passwordLogin.mutate() }} className={`grid gap-1.5 ${providerEntries.length || devStart ? 'border-t border-line pt-3' : ''}`}>
        <label className="text-[11px] font-medium text-ink-2" htmlFor="signin-username">Username</label>
        <input id="signin-username" required autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
        <label className="text-[11px] font-medium text-ink-2" htmlFor="signin-password">Password</label>
        <input id="signin-password" type="password" required autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
        {passwordLogin.error && <p role="alert" className="m-0 text-[11px] text-red">{passwordLogin.error.message}</p>}
        <button type="submit" disabled={passwordLogin.isPending} className="mt-1 h-9 rounded-[4px] bg-accent text-[12px] font-medium text-page disabled:opacity-50">{passwordLogin.isPending ? 'Signing in…' : 'Sign in'}</button>
      </form>}

      {legacyOnly && <form onSubmit={(event) => { event.preventDefault(); tokenLogin.mutate() }} className="grid gap-1.5">
        <label className="text-[11px] font-medium text-ink-2" htmlFor="login-token">Personal access token</label>
        <input id="login-token" type="password" autoComplete="off" required value={token} onChange={(event) => setToken(event.target.value)} className="h-10 w-full rounded-[4px] border-0 bg-field px-3 font-mono text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
        {tokenLogin.error && <p role="alert" className="m-0 text-[11px] text-red">{tokenLogin.error.message}</p>}
        <button type="submit" disabled={tokenLogin.isPending} className="mt-1 h-10 w-full rounded-[4px] bg-accent text-[12px] font-medium text-page disabled:opacity-50">{tokenLogin.isPending ? 'Signing in…' : 'Sign in'}</button>
        <p className="mb-0 mt-1 text-[10.5px] leading-4 text-ink-3">CLI: <code>rgt auth login &lt;server-url&gt;</code></p>
      </form>}
    </div>
  </section></main>
}

const slugify = (value: string) => value.toLowerCase().trim().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 48)

function CreateOrgScreen({ onReady }: { onReady: () => Promise<unknown> }) {
  const navigate = useNavigate()
  const [displayName, setDisplayName] = useState('')
  const [slug, setSlug] = useState('')
  const [slugEdited, setSlugEdited] = useState(false)
  const createOrg = useMutation({
    mutationFn: () => api.createOrg(slug, displayName),
    onSuccess: async (org) => { await onReady(); navigate(onboardingPathFor(org, 'managed')) },
  })
  return <main className="flex min-h-screen items-center justify-center bg-page p-4 text-ink"><form onSubmit={(event) => { event.preventDefault(); createOrg.mutate() }} className="w-full max-w-sm overflow-hidden rounded-[8px] border border-line bg-canvas shadow-raised">
    <header className="border-b border-line px-5 py-4"><span className="regent-kicker">Get started</span><h1 className="mb-0 mt-1 text-[16px] font-semibold">Create an organization</h1></header>
    <div className="grid gap-3 p-5">
      <label className="text-[11px] font-medium text-ink-2" htmlFor="org-name">Display name<input id="org-name" required value={displayName} onChange={(event) => { const value = event.target.value; setDisplayName(value); if (!slugEdited) setSlug(slugify(value)) }} className="mt-1.5 h-10 w-full rounded-[4px] border-0 bg-field px-3 text-[12.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" /></label>
      <label className="text-[11px] font-medium text-ink-2" htmlFor="org-slug">Slug<input id="org-slug" required pattern="[a-z0-9-]+" value={slug} onChange={(event) => { setSlugEdited(true); setSlug(slugify(event.target.value)) }} className="mt-1.5 h-10 w-full rounded-[4px] border-0 bg-field px-3 font-mono text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" /></label>
      {createOrg.error && <p role="alert" className="m-0 text-[11px] text-red">{createOrg.error.message}</p>}
      <button type="submit" disabled={createOrg.isPending || !slug || !displayName} className="mt-1 h-10 rounded-[4px] bg-accent text-[12px] font-medium text-page disabled:opacity-50">{createOrg.isPending ? 'Creating…' : 'Create organization'}</button>
    </div>
  </form></main>
}

function invitationErrorMessage(error: unknown) {
  const code = error instanceof ApiError && error.body && typeof error.body === 'object' && 'code' in error.body ? String((error.body as { code: unknown }).code) : undefined
  if (code === 'invitation_expired') return 'This invitation link has expired.'
  if (code === 'invitation_revoked') return 'This invitation has been revoked.'
  return 'This invitation link is not valid.'
}

function InvitationScreen({ token }: { token: string }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const invitation = useQuery({ queryKey: ['invitation', token], queryFn: () => api.invitation(token), retry: false })
  const capabilities = useQuery({ queryKey: ['capabilities'], queryFn: api.capabilities, retry: false })
  const [displayName, setDisplayName] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  useEffect(() => { if (invitation.data?.username) setUsername((current) => current || invitation.data!.username!) }, [invitation.data?.username])
  const accept = useMutation({
    mutationFn: () => api.acceptInvitation(token, { display_name: displayName, username: username || undefined, password: password || undefined }),
    onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: ['auth-me'] }); navigate('/', { replace: true }) },
  })

  if (invitation.isPending) return <main className="flex min-h-screen bg-page text-ink"><Pending label="Loading invitation…" /></main>
  if (invitation.error) return <main className="flex min-h-screen items-center justify-center bg-page p-4 text-ink"><p className="max-w-sm text-center text-[12.5px] text-ink-3">{invitationErrorMessage(invitation.error)}</p></main>

  const data = invitation.data
  const providerMethods = data.methods.filter((method) => method !== 'password')
  const authStarts = capabilities.data?.auth_starts ?? {}

  return <main className="flex min-h-screen items-center justify-center bg-page p-4 text-ink"><section className="w-full max-w-sm overflow-hidden rounded-[8px] border border-line bg-canvas shadow-raised">
    <header className="border-b border-line px-5 py-4"><span className="regent-kicker">Invitation</span><h1 className="mb-0 mt-1 text-[16px] font-semibold">Join {data.org_display_name}</h1><p className="mb-0 mt-1 text-[11.5px] text-ink-3">{data.email ? `For ${data.email}` : data.username ? `For @${data.username}` : 'Accept this invitation to join.'}</p></header>
    <div className="grid gap-3 p-5">
      {providerMethods.map((method) => authStarts[method] && <a key={method} href={withAuthParams(authStarts[method], '/', token)} className="flex h-10 w-full items-center justify-center rounded-[4px] bg-field text-[12.5px] font-medium shadow-hairline hover:bg-hover-2">Continue with {providerLabel(method)}</a>)}
      {data.methods.includes('password') && <form onSubmit={(event) => { event.preventDefault(); accept.mutate() }} className={`grid gap-1.5 ${providerMethods.length ? 'border-t border-line pt-3' : ''}`}>
        <label className="text-[11px] font-medium text-ink-2" htmlFor="invite-name">Display name<input id="invite-name" required value={displayName} onChange={(event) => setDisplayName(event.target.value)} className="mt-1.5 h-9 w-full rounded-[4px] border-0 bg-field px-2.5 text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" /></label>
        <label className="text-[11px] font-medium text-ink-2" htmlFor="invite-username">Username<input id="invite-username" required pattern="[a-z0-9][a-z0-9._-]*" value={username} onChange={(event) => setUsername(event.target.value)} className="mt-1.5 h-9 w-full rounded-[4px] border-0 bg-field px-2.5 text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" /></label>
        <label className="text-[11px] font-medium text-ink-2" htmlFor="invite-password">Password<input id="invite-password" type="password" required minLength={12} value={password} onChange={(event) => setPassword(event.target.value)} className="mt-1.5 h-9 w-full rounded-[4px] border-0 bg-field px-2.5 text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" /></label>
        {accept.error && <p role="alert" className="m-0 text-[11px] text-red">{accept.error.message}</p>}
        <button type="submit" disabled={accept.isPending} className="mt-1 h-9 rounded-[4px] bg-accent text-[12px] font-medium text-page disabled:opacity-50">{accept.isPending ? 'Joining…' : 'Accept invitation'}</button>
      </form>}
    </div>
  </section></main>
}

function InvitationRoute() {
  const { token = '' } = useParams()
  return <InvitationScreen token={token} />
}

function NotInvitedScreen() {
  const location = useLocation()
  const reason = new URLSearchParams(location.search).get('reason')
  return <main className="flex min-h-screen items-center justify-center bg-page p-4 text-ink"><div className="max-w-sm text-center"><h1 className="m-0 text-[16px] font-semibold">Your account is not invited</h1><p className="mt-2 text-[12px] leading-5 text-ink-3">{reason ? `${reason}. ` : ''}Ask an administrator to invite you to this organization.</p></div></main>
}

function SignInErrorScreen() {
  const location = useLocation()
  const code = new URLSearchParams(location.search).get('code')
  return <main className="flex min-h-screen items-center justify-center bg-page p-4 text-ink"><div className="max-w-sm text-center"><h1 className="m-0 text-[16px] font-semibold">Sign-in failed</h1><p className="mt-2 text-[12px] leading-5 text-ink-3">{code ? `Error: ${code}. ` : ''}Try signing in again, or contact an administrator.</p></div></main>
}

function DeviceApprovalScreen() {
  const location = useLocation()
  const prefill = new URLSearchParams(location.search).get('code') || ''
  const [userCode, setUserCode] = useState(prefill)
  const approve = useMutation({ mutationFn: () => api.approveDevice(userCode) })
  const errorMessage = approve.error instanceof ApiError && approve.error.status === 404 ? 'That code was not recognized.' : approve.error instanceof ApiError && approve.error.status === 403 ? 'This account cannot approve that device.' : approve.error?.message
  return <main className="flex min-h-screen items-center justify-center bg-page p-4 text-ink"><form onSubmit={(event) => { event.preventDefault(); approve.mutate() }} className="w-full max-w-sm overflow-hidden rounded-[8px] border border-line bg-canvas shadow-raised">
    <header className="border-b border-line px-5 py-4"><h1 className="m-0 text-[16px] font-semibold">Approve this device</h1><p className="mb-0 mt-1 text-[11.5px] text-ink-3">Enter the code shown on the device you are signing in.</p></header>
    <div className="grid gap-3 p-5">
      <label className="text-[11px] font-medium text-ink-2" htmlFor="device-code">Device code</label>
      <input id="device-code" required autoComplete="off" value={userCode} onChange={(event) => setUserCode(event.target.value)} className="h-10 rounded-[4px] border-0 bg-field px-3 text-center font-mono text-[16px] uppercase tracking-[0.15em] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
      {approve.isSuccess && <p role="status" className="m-0 text-[11.5px] text-green">Device approved. You can return to it now.</p>}
      {errorMessage && <p role="alert" className="m-0 text-[11px] text-red">{errorMessage}</p>}
      <button type="submit" disabled={approve.isPending || approve.isSuccess} className="mt-1 h-10 rounded-[4px] bg-accent text-[12px] font-medium text-page disabled:opacity-50">{approve.isPending ? 'Approving…' : approve.isSuccess ? 'Approved' : 'Approve device'}</button>
    </div>
  </form></main>
}

type ProjectOption = { id: string; display_name: string }

function RepoHome() {
  const navigate = useNavigate()
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const projects = useQuery({
    queryKey: ['projects'],
    queryFn: async (): Promise<ProjectOption[]> => {
      try {
        const response = await api.listProjects()
        return response.projects.map((project) => ({ id: project.id, display_name: project.display_name || project.id }))
      } catch (error) {
        // Older servers only expose the bare-id /repos list.
        if (error instanceof ApiError && error.status === 404) return (await api.listRepos()).repos.map((id) => ({ id, display_name: id }))
        throw error
      }
    },
    retry: false,
    refetchInterval: 1_500,
  })
  const copyCommand = async () => {
    try {
      await navigator.clipboard.writeText(connectCommand)
      setCopyState('copied')
    } catch {
      setCopyState('failed')
    }
  }
  if (projects.isPending) return <main className="flex min-h-screen bg-page text-ink"><Pending label="Connecting to re_gent…" /></main>
  if (projects.error) return <main className="flex min-h-screen bg-page text-ink"><Problem error={projects.error} onRetry={() => projects.refetch()} /></main>
  if (defaultRepo && projects.data.some((project) => project.id === defaultRepo)) return <Navigate replace to={`/repos/${defaultRepo}/sessions`} />
  if (projects.data.length === 1) return <Navigate replace to={`/repos/${projects.data[0].id}/sessions`} />
  const hasRepos = projects.data.length > 0
  return <main className="flex min-h-screen items-center justify-center bg-page p-4 text-ink">
    <section className="w-full max-w-md overflow-hidden rounded-[8px] border border-line bg-canvas shadow-raised">
      <header className="flex items-center gap-2 border-b border-line px-4 py-2.5">
        <img src="/favicon.svg" alt="" className="size-7" />
        <div>
          <h1 className="m-0 text-[15px] font-semibold leading-5">{hasRepos ? 'Open a re_gent repository' : 'Connect a project'}</h1>
          <p className="m-0 text-[11px] leading-4 text-ink-3">{hasRepos ? 'Repositories registered on this server' : 'Run one command from your project directory'}</p>
        </div>
      </header>
      {hasRepos && <div className="p-2">{projects.data.map((project) => <button key={project.id} onClick={() => navigate(`/repos/${project.id}/sessions`)} className="flex h-10 w-full items-center rounded-[4px] px-2.5 text-left text-[12.5px] hover:bg-hover"><span className="size-1.5 rounded-full bg-green" /><span className="ml-2 flex-1 font-medium">{project.display_name}</span><span className="text-ink-3">Open →</span></button>)}</div>}
      <div className={`${hasRepos ? 'border-t border-line' : ''} p-4`}>
        {hasRepos && <h2 className="m-0 text-[12px] font-medium">Connect another project</h2>}
        <p className={`${hasRepos ? 'mt-1' : 'mt-0'} mb-2.5 text-[11.5px] leading-5 text-ink-3`}>Open a terminal in the project you want re_gent to track, then run:</p>
        <div className="flex items-center overflow-hidden rounded-[8px] bg-inset shadow-hairline">
          <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap px-3 py-2.5 font-mono text-[11.5px] text-ink-2">{connectCommand}</code>
          <button type="button" onClick={() => void copyCommand()} aria-label="Copy connect command" className="mr-1.5 flex h-7 shrink-0 items-center gap-1.5 rounded-[4px] bg-field px-2 text-[10.5px] font-medium text-ink-2 shadow-hairline hover:bg-hover-2 hover:text-ink">
            {copyState === 'copied' ? <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden><path d="m2.5 6.2 2.1 2.1 4.9-5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" /></svg> : <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden><rect x="4" y="2" width="5.5" height="6" rx="1" stroke="currentColor" /><path d="M7.5 9v.25c0 .69-.56 1.25-1.25 1.25h-3.5c-.69 0-1.25-.56-1.25-1.25v-4C1.5 4.56 2.06 4 2.75 4H3" stroke="currentColor" /></svg>}
            {copyState === 'copied' ? 'Copied' : copyState === 'failed' ? 'Copy failed' : 'Copy'}
          </button>
        </div>
        <div role="status" aria-live="polite" className="mt-3 flex items-center gap-2 rounded-[4px] bg-accent-tint px-2.5 py-2 text-[11px] text-accent-ink">
          <span className="relative flex size-2" aria-hidden><span className="absolute inline-flex size-full animate-ping rounded-full bg-accent opacity-50" /><span className="relative inline-flex size-2 rounded-full bg-accent" /></span>
          <span>Listening for a connected project…</span>
        </div>
        <p className="mb-0 mt-2 text-[10.5px] leading-4 text-ink-3">Keep this page open. re_gent will continue automatically when the project appears.</p>
      </div>
    </section>
  </main>
}

function Shell() {
  const { repoId = '' } = useParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const location = useLocation()
  const viewer = queryClient.getQueryData<AuthMeResponse>(['auth-me'])?.viewer
  const active = viewFor(location.pathname)
  const settingsSection = settingsFor(location.pathname)
  const [sidebarWidth, setSidebarWidth] = usePersistentPanelSize('workspace-sidebar', 216, 64, 360)
  const [expandedSidebarWidth, setExpandedSidebarWidth] = useState(Math.max(216, sidebarWidth))
  const sidebarCollapsed = sidebarWidth <= 72
  const resizeSidebar = (next: number) => {
    if (next < 96) setSidebarWidth(64)
    else { setSidebarWidth(next); setExpandedSidebarWidth(next) }
  }
  const setSidebarCollapsed = (collapsed: boolean) => {
    if (collapsed) { setExpandedSidebarWidth(Math.max(180, sidebarWidth)); setSidebarWidth(64) }
    else setSidebarWidth(expandedSidebarWidth)
  }
  const navigateSettings = (section: SettingsView) => navigate(`/repos/${encodeURIComponent(repoId)}/settings/${section}`)

  return <div className="flex h-screen min-h-[560px] overflow-hidden bg-page text-ink">
    <div className="shrink-0 transition-[width] duration-150 motion-reduce:transition-none max-sm:hidden" style={{ width: sidebarWidth }}><ProjectSidebar fill project={repoId} active={active} settingsSection={settingsSection} collapsed={sidebarCollapsed} onCollapsedChange={setSidebarCollapsed} onNavigate={(view) => navigate(pathFor(repoId, view))} onSettingsNavigate={navigateSettings} userName={viewer?.display_name} userDetail={viewer ? `@${viewer.username}${viewer.instance_owner ? ' · owner' : ''}` : undefined} onUserClick={() => navigateSettings('general')} /></div>
    <ResizeHandle label="Resize navigation sidebar" value={sidebarWidth} min={64} max={360} defaultValue={216} onChange={resizeSidebar} className="max-sm:hidden" />
    <main className="flex min-w-0 flex-1 flex-col"><div key={active} className="regent-view flex min-h-0 flex-1"><Routes>
      <Route path="sessions/:sessionId?" element={<SessionsScreen repoId={repoId} />} />
      <Route path="conversations/:sessionId?" element={<LegacySessionRedirect repoId={repoId} />} />
      <Route path="team" element={<TeamDashboard repoId={repoId} />} />
      <Route path="files" element={<FilesScreen repoId={repoId} />} />
      <Route path="skills" element={<SkillsScreen />} />
      <Route path="sync" element={<Navigate replace to={`/repos/${encodeURIComponent(repoId)}/settings/status`} />} />
      <Route path="settings/:section?" element={<SettingsRoute repoId={repoId} />} />
      <Route index element={<Navigate replace to="sessions" />} />
    </Routes></div><nav className="hidden h-11 shrink-0 items-center justify-around border-t border-line bg-canvas max-sm:flex">{(['sessions', 'team', 'files', 'skills', 'settings'] as RegentView[]).map((item) => <button key={item} onClick={() => navigate(pathFor(repoId, item))} className={`px-2 text-[11px] capitalize ${active === item ? 'text-accent-ink' : 'text-ink-3'}`}>{item}</button>)}</nav></main>
  </div>
}

function LegacySessionRedirect({ repoId }: { repoId: string }) {
  const { sessionId } = useParams()
  return <Navigate replace to={`/repos/${repoId}/sessions${sessionId ? `/${encodeURIComponent(sessionId)}` : ''}`} />
}

function SessionsScreen({ repoId }: { repoId: string }) {
  const { sessionId: routeSessionId } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  const [listWidth, setListWidth] = usePersistentPanelSize('sessions-list', 340, 260, 560)
  // Set when arriving from a blamed line in Browse: the step to scroll to and open.
  const focusStep = new URLSearchParams(location.search).get('step') || undefined
  const sessions = useQuery({ queryKey: ['sessions', repoId], queryFn: () => api.sessions(repoId), retry: false })
  const memberFor = (session: NonNullable<typeof sessions.data>['sessions'][number]) => session.author?.name || session.author?.email || 'Unknown author'
  const visibleSessions = sessions.data?.sessions ?? []
  const sessionId = routeSessionId && visibleSessions.some((session) => session.session_id === routeSessionId) ? routeSessionId : visibleSessions[0]?.session_id
  const transcript = useQuery({ queryKey: ['transcript', repoId, sessionId], queryFn: async () => { try { return { kind: 'transcript' as const, data: await api.transcript(repoId, sessionId!) } } catch (error) { if (error instanceof ApiError && error.status === 404) return { kind: 'log' as const, data: await api.log(repoId, sessionId!) }; throw error } }, enabled: Boolean(sessionId), retry: false, refetchInterval: 7_500 })
  if (sessions.isPending) return <Pending />
  if (sessions.error) return <Problem error={sessions.error} onRetry={() => sessions.refetch()} />
  const entries = transcript.data ? transcript.data.kind === 'transcript' ? transcriptToEntries(transcript.data.data) : logToTranscript(transcript.data.data) : []
  const selected = sessions.data?.sessions.find((item) => item.session_id === sessionId)
  return <div className="flex min-h-0 flex-1 max-sm:flex-col">
    <div className="min-h-0 shrink-0 overflow-hidden max-sm:!h-[42%] max-sm:!w-full" style={{ width: listWidth }}><SessionSearch sessions={visibleSessions} selectedId={sessionId} onSelect={(id) => navigate(`/repos/${encodeURIComponent(repoId)}/sessions/${encodeURIComponent(id)}`)} /></div>
    <ResizeHandle label="Resize session list" value={listWidth} min={260} max={560} defaultValue={340} onChange={setListWidth} className="max-sm:hidden" />
    <section key={sessionId || 'empty'} className="regent-view min-h-0 min-w-0 flex-1 overflow-auto bg-canvas">
      {!sessionId ? <div className="flex min-h-full"><Empty title="No captured sessions yet" detail="Initialize this repository with rgt, enable the agent hooks, and complete one tool-using turn." /></div>
        : <><div className="sticky top-0 z-10 flex min-h-[56px] items-center gap-3 border-b border-line bg-canvas/95 px-4 py-2 backdrop-blur"><div className="min-w-0 flex-1"><h1 className="m-0 truncate text-[14px] font-semibold leading-5">{selected?.title || 'Captured session'}</h1><div className="flex flex-wrap items-center gap-x-2 text-[10.5px] leading-4 text-ink-3"><span className="font-mono text-accent-ink">{sessionId}</span><span className="inline-flex items-center gap-1"><span className="inline-flex" style={{ color: agentColor(selected?.agent_id) }}><AgentIcon origin={selected?.agent_id} decorative className="size-3.5" /></span>{agentLabel(selected?.agent_id)}</span><span>{selected?.author ? memberFor(selected) : 'Unknown author'}</span><span>{selected?.step_count ?? entries.filter((item) => item.type === 'step').length} steps</span></div></div></div>
          {transcript.isPending ? <div className="flex min-h-[360px]"><Pending label="Reconstructing transcript…" /></div>
            : transcript.error ? <div className="flex min-h-[360px]"><Problem error={transcript.error} onRetry={() => transcript.refetch()} /></div>
              : entries.length ? <ConversationTranscript entries={entries} focusStep={focusStep} repoId={repoId} /> : <div className="flex min-h-[360px]"><Empty title="No transcript content" detail="The session exists, but its recorded steps do not contain readable conversation events." /></div>}</>}
    </section>
  </div>
}

function useLatestLog(repoId: string) {
  const sessions = useQuery({ queryKey: ['sessions', repoId], queryFn: () => api.sessions(repoId), retry: false })
  const session = sessions.data?.sessions[0]?.session_id
  const log = useQuery({ queryKey: ['log', repoId, session], queryFn: () => api.log(repoId, session!), enabled: Boolean(session), retry: false })
  return { sessions, session, log }
}

function FilesScreen({ repoId }: { repoId: string }) {
  const location = useLocation(); const params = new URLSearchParams(location.search)
  const requestedStep = params.get('step') || undefined; const requestedPath = params.get('path') || undefined
  const [treeWidth, setTreeWidth] = usePersistentPanelSize('files-tree', 320, 240, 560)
  const { sessions, session, log } = useLatestLog(repoId); const step = requestedStep || log.data?.steps[0]?.hash
  const files = useQuery({ queryKey: ['files', repoId, step, session], queryFn: () => api.files(repoId, { step, session }), enabled: Boolean(step || session), retry: false })
  const [selected, setSelected] = useState<string>()
  // A fresh ?path= link must win over whatever the user last clicked in the tree.
  useEffect(() => { setSelected(undefined) }, [requestedPath, requestedStep])
  const path = selected || requestedPath || files.data?.files[0]?.path
  // Shares the ['step', ...] cache with BlameView, so linking back to the session that
  // produced this tree costs no extra request.
  const stepDetail = useQuery({ queryKey: ['step', repoId, step], queryFn: () => api.step(repoId, step!), enabled: Boolean(step), retry: false })
  const stepSession = stepDetail.data?.session_id
  const blame = useQuery({ queryKey: ['blame', repoId, files.data?.step_hash, path], queryFn: () => api.blame(repoId, files.data!.step_hash, path!), enabled: Boolean(files.data?.step_hash && path), retry: false })
  if (sessions.isPending || (session && log.isPending) || ((step || session) && files.isPending)) return <Pending label="Reading captured tree…" />
  const error = sessions.error || log.error || files.error; if (error) return <Problem error={error} onRetry={() => files.refetch()} />
  if (!files.data?.files.length) return <Empty title="No captured files" detail="Choose a step with a workspace tree, or complete a captured agent turn first." />
  return <div className="flex min-h-0 flex-1 max-md:flex-col"><aside className="min-h-0 shrink-0 overflow-auto bg-canvas max-md:!h-[38%] max-md:!w-full" style={{ width: treeWidth }}><div className="sticky top-0 z-10 flex h-10 items-center border-b border-line bg-canvas px-3 text-[12.5px] font-semibold">Files<span className="ml-auto text-[10.5px] font-normal tabular-nums text-ink-3">{files.data.total_files}</span></div><FileTree files={files.data.files} selectedPath={path} onSelect={setSelected} /></aside><ResizeHandle label="Resize file tree" value={treeWidth} min={240} max={560} defaultValue={320} onChange={setTreeWidth} className="max-md:hidden" /><section className="flex min-w-0 flex-1 flex-col overflow-hidden bg-inset"><div className="z-10 flex h-10 shrink-0 items-center border-b border-line bg-canvas px-3"><span className="truncate font-mono text-[11.5px]">{path}</span>{path && <span className="ml-2 shrink-0 font-mono text-[10px] text-ink-3">{languageForPath(path)}</span>}{stepSession ? <Link to={`/repos/${encodeURIComponent(repoId)}/sessions/${encodeURIComponent(stepSession)}?step=${encodeURIComponent(files.data.step_hash)}`} aria-label={`Open the session that produced step ${short(files.data.step_hash)}`} className="ml-auto font-mono text-[10.5px] text-accent-ink underline-offset-2 hover:underline">{short(files.data.step_hash)}</Link> : <span className="ml-auto font-mono text-[10.5px] text-accent-ink">{short(files.data.step_hash)}</span>}</div>{blame.isPending ? <Pending label="Loading provenance…" /> : blame.error ? <Problem error={blame.error} onRetry={() => blame.refetch()} /> : <BlameView repoId={repoId} data={blame.data!} />}</section></div>
}

function StatusScreen({ repoId }: { repoId: string }) {
  const status = useQuery({ queryKey: ['status', repoId], queryFn: () => api.status(repoId), retry: false, refetchInterval: 10_000 })
  if (status.isPending) return <Pending label="Checking server…" />
  if (status.error) return <Problem error={status.error} onRetry={() => status.refetch()} />
  const data: StatusResponse = status.data; const service = typeof data.service === 'string' ? data.service : [data.service.name || 're_gent', apiVersionOf(data)].filter(Boolean).join(' · '); const repo = data.repository || {}
  const rows = [['Repository', repo.id || repoId], ['Service', service], ['Objects', repo.object_count ?? '—'], ['Refs', repo.ref_count ?? '—'], ['Sessions', repo.session_count ?? '—'], ['Last activity', repo.last_activity ? new Date(repo.last_activity).toLocaleString() : '—']]
  return <section className="mx-auto w-full max-w-[720px] p-5"><div className="mb-3 flex items-start justify-between"><div><h1 className="m-0 text-[16px] font-semibold leading-5">Server status</h1><p className="m-0 text-[11.5px] leading-4 text-ink-3">Repository storage and capture availability.</p></div><span className={`flex items-center gap-1.5 text-[11.5px] ${data.status === 'ok' ? 'text-green' : 'text-red'}`}><span className={`size-1.5 rounded-full ${data.status === 'ok' ? 'bg-green' : 'bg-red'}`} />{data.status}</span></div><div className="overflow-hidden rounded-[8px] border border-line">{rows.map(([label, value]) => <div key={label} className="grid grid-cols-[130px_1fr] border-b border-line text-[12px] last:border-0"><div className="bg-inset px-3 py-2 text-ink-3">{label}</div><div className="px-3 py-2 font-mono text-ink-2">{value}</div></div>)}</div></section>
}

function SettingsRoute({ repoId }: { repoId: string }) {
  const { section = 'general' } = useParams()
  if (section === 'status') return <StatusScreen repoId={repoId} />
  if (!['general', 'users', 'data'].includes(section)) return <Navigate replace to={`/repos/${encodeURIComponent(repoId)}/settings/general`} />
  return <SettingsScreen section={section as SettingsSection} repoId={repoId} />
}

function ProductRoutes() {
  return <Routes>
    <Route path="/" element={<RepoHome />} />
    <Route path="/invitations/:token" element={<InvitationRoute />} />
    <Route path="/not-invited" element={<NotInvitedScreen />} />
    <Route path="/sign-in-error" element={<SignInErrorScreen />} />
    <Route path="/device" element={<DeviceApprovalScreen />} />
    <Route path="/setup/*" element={<OnboardingRoutes />} />
    <Route path="/o/:slug/setup/*" element={<OnboardingRoutes />} />
    <Route path="/repos/:repoId/*" element={<Shell />} />
    <Route path="*" element={<Navigate replace to="/" />} />
  </Routes>
}

export default function App() { return <AuthGate><ProductRoutes /></AuthGate> }
