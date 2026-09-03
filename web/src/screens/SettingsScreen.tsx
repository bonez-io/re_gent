import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../api/client'
import type { AuthMeResponse, ProjectRole } from '../api/types'

export type SettingsSection = 'general' | 'users' | 'data'

const copy: Record<Exclude<SettingsSection, 'users'>, { title: string; detail: string; rows: Array<[string, string]> }> = {
  general: { title: 'General', detail: 'Workspace behavior and personal preferences.', rows: [['Appearance', 'System default'], ['Default landing view', 'Sessions'], ['Notifications', 'Not configured']] },
  data: { title: 'Data', detail: 'Retention, storage, export, and indexing controls.', rows: [['Storage', 'Local re_gent store'], ['Retention', 'Keep all captured history'], ['Semantic index', 'Not connected'], ['Export and backup', 'Coming later']] },
}

const roles: ProjectRole[] = ['owner', 'admin', 'writer', 'reader']

export function SettingsScreen({ section, repoId = '' }: { section: SettingsSection; repoId?: string }) {
  if (section === 'users') return <AccessSettings repoId={repoId} />
  if (section === 'general') return <GeneralSettings />
  const content = copy[section]
  return <section className="min-h-0 flex-1 overflow-auto bg-page p-6 text-ink">
    <div className="mx-auto max-w-[760px]">
      <span className="regent-kicker">Settings</span>
      <h1 className="mb-0 mt-1 text-[20px] font-semibold tracking-[-0.02em]">{content.title}</h1>
      <p className="mb-5 mt-1 text-[12px] text-ink-3">{content.detail}</p>
      <div className="overflow-hidden rounded-[8px] border border-line bg-canvas shadow-hairline">
        {content.rows.map(([label, value]) => <div key={label} className="grid grid-cols-[190px_minmax(0,1fr)] items-center border-b border-line px-4 py-3 text-[12px] last:border-0 max-sm:grid-cols-1 max-sm:gap-1"><span className="font-medium text-ink-2">{label}</span><span className="text-ink-3">{value}</span></div>)}
      </div>
    </div>
  </section>
}

function GeneralSettings() {
  const queryClient = useQueryClient()
  const viewer = queryClient.getQueryData<AuthMeResponse>(['auth-me'])?.viewer
  const logout = useMutation({ mutationFn: api.logout, onSuccess: () => queryClient.resetQueries({ queryKey: ['auth-me'] }) })
  const content = copy.general
  return <section className="min-h-0 flex-1 overflow-auto bg-page p-6 text-ink">
    <div className="mx-auto max-w-[760px]">
      <span className="regent-kicker">Settings</span>
      <h1 className="mb-0 mt-1 text-[20px] font-semibold tracking-[-0.02em]">{content.title}</h1>
      <p className="mb-5 mt-1 text-[12px] text-ink-3">{content.detail}</p>
      <div className="overflow-hidden rounded-[8px] border border-line bg-canvas shadow-hairline">
        {content.rows.map(([label, value]) => <div key={label} className="grid grid-cols-[190px_minmax(0,1fr)] items-center border-b border-line px-4 py-3 text-[12px] last:border-0 max-sm:grid-cols-1 max-sm:gap-1"><span className="font-medium text-ink-2">{label}</span><span className="text-ink-3">{value}</span></div>)}
      </div>
      {viewer && <div className="mt-4 flex items-center justify-between gap-4 rounded-[8px] border border-line bg-canvas p-4 shadow-hairline"><div className="min-w-0"><h2 className="m-0 truncate text-[13px] font-semibold">{viewer.display_name}</h2><p className="m-0 truncate text-[11px] text-ink-3">@{viewer.username}{viewer.instance_owner ? ' · instance owner' : ''}</p></div><button type="button" disabled={logout.isPending} onClick={() => logout.mutate()} className="h-9 shrink-0 rounded-[4px] bg-field px-3 text-[11.5px] font-medium shadow-hairline hover:bg-hover-2 disabled:opacity-40">{logout.isPending ? 'Signing out…' : 'Sign out'}</button></div>}
      {logout.error && <p role="alert" className="mt-3 text-[11px] text-red">{logout.error.message}</p>}
    </div>
  </section>
}

function AccessSettings({ repoId }: { repoId: string }) {
  const queryClient = useQueryClient()
  const members = useQuery({ queryKey: ['access-members', repoId], queryFn: () => api.members(repoId), enabled: Boolean(repoId), retry: false })
  const users = useQuery({ queryKey: ['access-users'], queryFn: api.users, retry: false })
  const tokens = useQuery({ queryKey: ['access-tokens'], queryFn: api.tokens, retry: false })
  const [selectedUser, setSelectedUser] = useState('')
  const [selectedRole, setSelectedRole] = useState<ProjectRole>('reader')
  const [username, setUsername] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [tokenName, setTokenName] = useState('developer machine')
  const [tokenDays, setTokenDays] = useState(30)
  const [issuedToken, setIssuedToken] = useState<{ name: string; token: string }>()
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')

  const refreshMembers = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['access-members', repoId] }),
      queryClient.invalidateQueries({ queryKey: ['access-users'] }),
    ])
  }
  const putMember = useMutation({ mutationFn: ({ userId, role }: { userId: string; role: ProjectRole }) => api.putMember(repoId, userId, role), onSuccess: refreshMembers })
  const removeMember = useMutation({ mutationFn: (userId: string) => api.deleteMember(repoId, userId), onSuccess: refreshMembers })
  const createMember = useMutation({
    mutationFn: () => api.createUser(username, displayName, repoId, selectedRole),
    onSuccess: async (created) => {
      setIssuedToken({ name: created.user.display_name, token: created.initial_token })
      setUsername(''); setDisplayName('')
      await refreshMembers()
    },
  })
  const createToken = useMutation({
    mutationFn: () => api.createToken(tokenName, tokenDays),
    onSuccess: async (created) => {
      setIssuedToken({ name: created.token.name, token: created.secret })
      setCopyState('idle')
      await queryClient.invalidateQueries({ queryKey: ['access-tokens'] })
    },
  })
  const revokeToken = useMutation({ mutationFn: api.revokeToken, onSuccess: () => queryClient.invalidateQueries({ queryKey: ['access-tokens'] }) })

  const assigned = new Set(members.data?.members.map((member) => member.id))
  const availableUsers = users.data?.users.filter((user) => !assigned.has(user.id)) ?? []
  const mutationError = putMember.error || removeMember.error || createMember.error || createToken.error || revokeToken.error
  const unavailable = members.error instanceof ApiError && members.error.status === 404

  const copyToken = async () => {
    if (!issuedToken) return
    try { await navigator.clipboard.writeText(issuedToken.token); setCopyState('copied') } catch { setCopyState('failed') }
  }

  return <section className="min-h-0 flex-1 overflow-auto bg-page p-6 text-ink">
    <div className="mx-auto max-w-[860px]">
      <span className="regent-kicker">Settings</span>
      <div className="mb-5 mt-1 flex items-start justify-between gap-4">
        <div><h1 className="m-0 text-[20px] font-semibold tracking-[-0.02em]">Access</h1><p className="mb-0 mt-1 text-[12px] text-ink-3">Members and project roles for <span className="font-mono text-ink-2">{repoId || 'this repository'}</span>.</p></div>
        {members.data && <span className="rounded-full bg-field px-2.5 py-1 text-[10.5px] text-ink-3 shadow-hairline">{members.data.members.length} members</span>}
      </div>

      {issuedToken && <div role="status" className="mb-4 rounded-[8px] border border-green/30 bg-green/10 p-4">
        <div className="flex items-start justify-between gap-3"><div><h2 className="m-0 text-[13px] font-semibold">Copy {issuedToken.name}&apos;s initial token now</h2><p className="mb-2 mt-1 text-[11px] text-ink-3">It is shown once. Send it through a secure channel, then ask them to rotate it.</p></div><button type="button" onClick={() => setIssuedToken(undefined)} aria-label="Dismiss initial token" className="text-ink-3 hover:text-ink">×</button></div>
        <div className="flex overflow-hidden rounded-[4px] bg-inset shadow-hairline"><code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap px-3 py-2 text-[11px]">{issuedToken.token}</code><button type="button" onClick={() => void copyToken()} className="m-1 rounded-[4px] bg-field px-2.5 text-[10.5px] shadow-hairline hover:bg-hover-2">{copyState === 'copied' ? 'Copied' : copyState === 'failed' ? 'Copy failed' : 'Copy'}</button></div>
      </div>}

      {members.isPending && <div className="rounded-[8px] border border-line bg-canvas p-5 text-[12px] text-ink-3">Loading project access…</div>}
      {unavailable && <div className="rounded-[8px] border border-line bg-canvas p-5"><h2 className="m-0 text-[13px] font-semibold">Access controls are unavailable</h2><p className="mb-0 mt-1 text-[11.5px] text-ink-3">This server is running the legacy open profile. Start it in self-hosted auth mode to manage users and roles.</p></div>}
      {members.error && !unavailable && <div role="alert" className="rounded-[8px] border border-red/30 bg-red/10 p-3 text-[11.5px] text-red">{members.error.message}</div>}
      {members.data && <div className="overflow-hidden rounded-[8px] border border-line bg-canvas shadow-hairline">
        <div className="grid grid-cols-[minmax(0,1fr)_150px_76px] border-b border-line bg-inset px-4 py-2 text-[10px] font-medium uppercase tracking-[0.08em] text-ink-3 max-sm:grid-cols-[1fr_110px] max-sm:[&>*:last-child]:hidden"><span>Member</span><span>Role</span><span /></div>
        {members.data.members.map((member) => <div key={member.id} className="grid min-h-14 grid-cols-[minmax(0,1fr)_150px_76px] items-center border-b border-line px-4 py-2 last:border-0 max-sm:grid-cols-[1fr_110px]">
          <div className="min-w-0"><div className="truncate text-[12px] font-medium">{member.display_name}</div><div className="truncate text-[10.5px] text-ink-3">@{member.username}{member.instance_owner ? ' · instance owner' : ''}</div></div>
          <select aria-label={`Role for ${member.display_name}`} value={member.role} disabled={member.instance_owner || putMember.isPending} onChange={(event) => { const role = event.target.value as ProjectRole; if ((member.role === 'owner' || role === 'owner') && !window.confirm(`Change ${member.display_name}'s project role from ${member.role} to ${role}?`)) return; putMember.mutate({ userId: member.id, role }) }} className="h-8 rounded-[4px] border-0 bg-field px-2 text-[11px] text-ink shadow-hairline disabled:opacity-60">{roles.map((role) => <option key={role} value={role}>{role}</option>)}</select>
          <button type="button" disabled={member.instance_owner || removeMember.isPending} onClick={() => { if (window.confirm(`Remove ${member.display_name} from this project?`)) removeMember.mutate(member.id) }} className="ml-2 h-8 rounded-[4px] text-[10.5px] text-ink-3 hover:bg-hover hover:text-red disabled:opacity-30 max-sm:hidden">Remove</button>
        </div>)}
      </div>}

      {users.data && <div className="mt-4 rounded-[8px] border border-line bg-canvas p-4 shadow-hairline">
        <h2 className="m-0 text-[13px] font-semibold">Add an existing user</h2><p className="mb-3 mt-1 text-[11px] text-ink-3">Assign a local account to this project.</p>
        <div className="grid grid-cols-[minmax(0,1fr)_130px_auto] gap-2 max-sm:grid-cols-1">
          <select aria-label="User to add" value={selectedUser} onChange={(event) => setSelectedUser(event.target.value)} className="h-9 rounded-[4px] border-0 bg-field px-2 text-[11.5px] text-ink shadow-hairline"><option value="">Select a user…</option>{availableUsers.map((user) => <option key={user.id} value={user.id}>{user.display_name} (@{user.username})</option>)}</select>
          <select aria-label="Role for new member" value={selectedRole} onChange={(event) => setSelectedRole(event.target.value as ProjectRole)} className="h-9 rounded-[4px] border-0 bg-field px-2 text-[11.5px] text-ink shadow-hairline">{roles.map((role) => <option key={role} value={role}>{role}</option>)}</select>
          <button type="button" disabled={!selectedUser || putMember.isPending} onClick={() => putMember.mutate({ userId: selectedUser, role: selectedRole }, { onSuccess: () => setSelectedUser('') })} className="h-9 rounded-[4px] bg-accent px-3 text-[11.5px] font-medium text-page disabled:opacity-40">Add member</button>
        </div>
      </div>}

      {users.data && <form className="mt-4 rounded-[8px] border border-line bg-canvas p-4 shadow-hairline" onSubmit={(event) => { event.preventDefault(); createMember.mutate() }}>
        <h2 className="m-0 text-[13px] font-semibold">Create a local user</h2><p className="mb-3 mt-1 text-[11px] text-ink-3">Creates an account, assigns the selected role, and issues a one-time initial token.</p>
        <div className="grid grid-cols-[1fr_1fr_130px_auto] gap-2 max-md:grid-cols-2 max-sm:grid-cols-1">
          <input aria-label="Username" required minLength={1} maxLength={64} pattern="[a-z0-9][a-z0-9._-]*" value={username} onChange={(event) => setUsername(event.target.value)} placeholder="username" className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
          <input aria-label="Display name" required maxLength={120} value={displayName} onChange={(event) => setDisplayName(event.target.value)} placeholder="Display name" className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
          <select aria-label="Role for created user" value={selectedRole} onChange={(event) => setSelectedRole(event.target.value as ProjectRole)} className="h-9 rounded-[4px] border-0 bg-field px-2 text-[11.5px] text-ink shadow-hairline">{roles.map((role) => <option key={role} value={role}>{role}</option>)}</select>
          <button type="submit" disabled={createMember.isPending} className="h-9 rounded-[4px] bg-field px-3 text-[11.5px] font-medium shadow-hairline hover:bg-hover-2 disabled:opacity-40">{createMember.isPending ? 'Creating…' : 'Create user'}</button>
        </div>
      </form>}
      {users.error instanceof ApiError && users.error.status === 403 && <p className="mt-3 text-[10.5px] text-ink-3">Only the instance owner can create local accounts. Project admins can still update existing memberships.</p>}

      {tokens.data && <div className="mt-4 rounded-[8px] border border-line bg-canvas p-4 shadow-hairline">
        <h2 className="m-0 text-[13px] font-semibold">Personal access tokens</h2><p className="mb-3 mt-1 text-[11px] text-ink-3">Use these with <code>rgt auth login</code>. Secrets are shown only when created.</p>
        <div className="overflow-hidden rounded-[4px] border border-line">{tokens.data.tokens.length === 0 ? <p className="m-0 px-3 py-3 text-[11px] text-ink-3">No active tokens.</p> : tokens.data.tokens.map((token) => <div key={token.id} className="grid grid-cols-[minmax(0,1fr)_110px_76px] items-center border-b border-line px-3 py-2 last:border-0 max-sm:grid-cols-[1fr_76px]">
          <div className="min-w-0"><div className="truncate text-[11.5px] font-medium">{token.name}</div><div className="truncate font-mono text-[10px] text-ink-3">{token.prefix}… · expires {new Date(token.expires_at).toLocaleDateString()}</div></div><span className="text-[10px] text-ink-3 max-sm:hidden">{token.last_used_at ? `used ${new Date(token.last_used_at).toLocaleDateString()}` : 'unused'}</span><button type="button" disabled={revokeToken.isPending} onClick={() => { if (window.confirm(`Revoke ${token.name}? This cannot be undone.`)) revokeToken.mutate(token.id) }} className="h-8 rounded-[4px] text-[10.5px] text-ink-3 hover:bg-hover hover:text-red disabled:opacity-40">Revoke</button>
        </div>)}</div>
        <form className="mt-3 grid grid-cols-[minmax(0,1fr)_110px_auto] gap-2 max-sm:grid-cols-1" onSubmit={(event) => { event.preventDefault(); createToken.mutate() }}><input aria-label="Token name" required maxLength={80} value={tokenName} onChange={(event) => setTokenName(event.target.value)} className="h-9 rounded-[4px] border-0 bg-field px-2.5 text-[11.5px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" /><select aria-label="Token lifetime" value={tokenDays} onChange={(event) => setTokenDays(Number(event.target.value))} className="h-9 rounded-[4px] border-0 bg-field px-2 text-[11px] shadow-hairline"><option value={7}>7 days</option><option value={30}>30 days</option><option value={90}>90 days</option><option value={365}>1 year</option></select><button type="submit" disabled={createToken.isPending} className="h-9 rounded-[4px] bg-field px-3 text-[11.5px] font-medium shadow-hairline hover:bg-hover-2 disabled:opacity-40">Create token</button></form>
      </div>}
      {mutationError && <p role="alert" className="mt-3 text-[11px] text-red">{mutationError.message}</p>}
    </div>
  </section>
}
