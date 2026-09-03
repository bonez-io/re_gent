import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { rememberCSRFToken } from '../../api/client'
import { onboardingApi, type DefaultRole, type JoinPolicy } from '../../api/onboarding'
import { OnboardingLayout } from './chrome'
import { onboardingPathFor } from './path'

const slugify = (value: string) => value.toLowerCase().trim().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 48)

/** Screen 1, self-hosted only: organization + admin, replacing the initial password. */
export function AdminScreen() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [orgName, setOrgName] = useState('')
  const [slug, setSlug] = useState('')
  const [slugTouched, setSlugTouched] = useState(false)
  const [serverUrl, setServerUrl] = useState(() => window.location.origin.replace(/\/+$/, ''))
  const [username, setUsername] = useState('admin')
  const [displayName, setDisplayName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [joinPolicy, setJoinPolicy] = useState<JoinPolicy>('invite_only')
  const [defaultRole, setDefaultRole] = useState<DefaultRole>('reader')

  const onOrgNameChange = (value: string) => {
    setOrgName(value)
    if (!slugTouched) setSlug(slugify(value))
  }

  const submit = useMutation({
    mutationFn: () => onboardingApi.submitAdmin({
      org: { display_name: orgName, slug, server_url: serverUrl, join_policy: joinPolicy, default_role: defaultRole },
      admin: { username, display_name: displayName, ...(email ? { email } : {}), new_password: password },
    }),
    onSuccess: async (response) => {
      rememberCSRFToken(response.csrf)
      await queryClient.invalidateQueries({ queryKey: ['onboarding-me'] })
      navigate(onboardingPathFor(response.org, 'self-hosted'))
    },
  })

  return <OnboardingLayout deployment="self-hosted" current="admin_password" title="Organization and admin" description="One organization per self-hosted instance. Saving this replaces the initial password.">
    <form className="grid gap-3" onSubmit={(event) => { event.preventDefault(); submit.mutate() }}>
      <label className="text-[11px] font-medium text-ink-2">Organization name
        <input required value={orgName} onChange={(event) => onOrgNameChange(event.target.value)} className="mt-1.5 h-10 w-full rounded-[4px] border-0 bg-field px-3 text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
      </label>
      <label className="text-[11px] font-medium text-ink-2">Slug
        <input required pattern="[a-z0-9][a-z0-9-]*" value={slug} onChange={(event) => { setSlug(slugify(event.target.value)); setSlugTouched(true) }} className="mt-1.5 h-10 w-full rounded-[4px] border-0 bg-field px-3 font-mono text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
      </label>
      <div>
        <label className="text-[11px] font-medium text-ink-2">Server address
          <input required value={serverUrl} onChange={(event) => setServerUrl(event.target.value)} className="mt-1.5 h-10 w-full rounded-[4px] border-0 bg-field px-3 font-mono text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
        </label>
        <span className="mt-1 block text-[10.5px] text-ink-3">Used in every command the wizard prints and in invitation links.</span>
      </div>
      <div className="grid grid-cols-2 gap-2 max-sm:grid-cols-1">
        <label className="text-[11px] font-medium text-ink-2">Admin username
          <input required pattern="[a-z0-9][a-z0-9._-]*" value={username} onChange={(event) => setUsername(event.target.value)} className="mt-1.5 h-10 w-full rounded-[4px] border-0 bg-field px-3 text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
        </label>
        <label className="text-[11px] font-medium text-ink-2">Admin display name
          <input required value={displayName} onChange={(event) => setDisplayName(event.target.value)} className="mt-1.5 h-10 w-full rounded-[4px] border-0 bg-field px-3 text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
        </label>
      </div>
      <label className="text-[11px] font-medium text-ink-2">Admin email (optional)
        <input type="email" value={email} onChange={(event) => setEmail(event.target.value)} className="mt-1.5 h-10 w-full rounded-[4px] border-0 bg-field px-3 text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
      </label>
      <div>
        <label className="text-[11px] font-medium text-ink-2">New password
          <input type="password" required minLength={12} autoComplete="new-password" value={password} onChange={(event) => setPassword(event.target.value)} className="mt-1.5 h-10 w-full rounded-[4px] border-0 bg-field px-3 font-mono text-[12px] shadow-hairline outline-none focus:ring-1 focus:ring-accent" />
        </label>
        <span className="mt-1 block text-[10.5px] text-ink-3">Minimum 12 characters. Replaces the initial password immediately.</span>
      </div>
      <div className="grid grid-cols-2 gap-2 max-sm:grid-cols-1">
        <label className="text-[11px] font-medium text-ink-2">Who can join
          <select value={joinPolicy} onChange={(event) => setJoinPolicy(event.target.value as JoinPolicy)} className="native-select mt-1.5 h-10"><option value="invite_only">Invite only</option><option value="open">Open — anyone with the server address</option></select>
        </label>
        <label className="text-[11px] font-medium text-ink-2">Default role for new members
          <select value={defaultRole} onChange={(event) => setDefaultRole(event.target.value as DefaultRole)} className="native-select mt-1.5 h-10"><option value="reader">Reader</option><option value="writer">Writer</option></select>
        </label>
      </div>
      {submit.error && <p role="alert" className="m-0 text-[11px] text-red">{submit.error.message}</p>}
      <button type="submit" disabled={submit.isPending} className="mt-1 h-10 rounded-[4px] bg-accent text-[12px] font-medium text-page disabled:opacity-50">{submit.isPending ? 'Creating organization…' : 'Continue'}</button>
    </form>
  </OnboardingLayout>
}
