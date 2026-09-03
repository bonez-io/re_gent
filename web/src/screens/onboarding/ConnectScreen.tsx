import { useEffect, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { onboardingApi, type Connection } from '../../api/onboarding'
import { OnboardingLayout, OnboardingPending, OnboardingProblem } from './chrome'
import { onboardingPathFor } from './path'
import { useOnboardingBase } from './shared'

function upsertConnections(existing: Connection[], incoming: Connection[]): Connection[] {
  if (!incoming.length) return existing
  const byProject = new Map(existing.map((connection) => [connection.project_id, connection]))
  for (const connection of incoming) byProject.set(connection.project_id, connection)
  return Array.from(byProject.values()).sort((a, b) => a.connected_at.localeCompare(b.connected_at))
}

/** Screen 2: connect repositories. A minted setup code plus a live, long-polled feed. */
export function ConnectScreen() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { deployment, slug, org, serverUrl } = useOnboardingBase()
  const [connections, setConnections] = useState<Connection[]>([])
  const [command, setCommand] = useState<string>()
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const stoppedRef = useRef(false)

  const mint = useMutation({
    mutationFn: () => onboardingApi.createSetupCode(slug!),
    onSuccess: (response) => { setCommand(response.command); setCopyState('idle') },
  })
  // Keep a stable reference so the mount effect below does not need `mint` itself in
  // its dependency array (a new mutate identity on every render would re-fire it).
  const mintRef = useRef(mint.mutate)
  mintRef.current = mint.mutate

  useEffect(() => {
    if (slug && !command) mintRef.current()
  }, [slug, command])

  // Long-polls the connections feed for the lifetime of this screen: each response
  // carries the cursor for the next call, and the server holds the request open up to
  // 25s waiting for a new row. Aborted on unmount so a stale loop cannot keep running
  // (and cannot update state) after the admin has moved on.
  useEffect(() => {
    if (!slug) return
    stoppedRef.current = false
    const controller = new AbortController()
    let cursor: string | undefined
    const loop = async () => {
      while (!stoppedRef.current) {
        try {
          const response = await onboardingApi.connections(slug, cursor, controller.signal)
          if (stoppedRef.current) return
          cursor = response.cursor
          setConnections((prev) => upsertConnections(prev, response.connections))
        } catch {
          if (stoppedRef.current) return
          await new Promise((resolve) => setTimeout(resolve, 2000))
        }
      }
    }
    void loop()
    return () => { stoppedRef.current = true; controller.abort() }
  }, [slug])

  const copyCommand = async () => {
    if (!command) return
    try { await navigator.clipboard.writeText(command); setCopyState('copied') } catch { setCopyState('failed') }
  }

  const advance = useMutation({
    mutationFn: () => onboardingApi.advance(slug!, 'users'),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['onboarding-org', slug] })
      navigate(onboardingPathFor({ slug: slug!, onboarding: 'users' }, deployment))
    },
  })

  if (!slug || org.isPending) return <OnboardingPending label="Loading organization…" />
  if (org.error) return <OnboardingProblem error={org.error} onRetry={() => org.refetch()} />

  return <OnboardingLayout deployment={deployment} current="connect" title="Connect repositories" description="Run one command from each project you want re_gent to track.">
    <div className="flex items-center overflow-hidden rounded-[8px] bg-inset shadow-hairline">
      <code tabIndex={0} className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap px-3 py-2.5 font-mono text-[11.5px] text-ink-2">{command ?? (mint.isPending ? 'Preparing a setup command…' : `curl -fsSL ${serverUrl}/install | sh && rgt connect ${serverUrl} --setup …`)}</code>
      <button type="button" onClick={() => void copyCommand()} disabled={!command} aria-label="Copy connect command" className="mr-1.5 flex h-7 shrink-0 items-center gap-1.5 rounded-[4px] bg-field px-2 text-[10.5px] font-medium text-ink-2 shadow-hairline hover:bg-hover-2 hover:text-ink disabled:opacity-40">
        {copyState === 'copied' ? 'Copied' : copyState === 'failed' ? 'Copy failed' : 'Copy'}
      </button>
    </div>
    {mint.error && <p role="alert" className="mt-2 text-[11px] text-red">{mint.error.message}</p>}
    <button type="button" onClick={() => mint.mutate()} disabled={mint.isPending} className="mt-2 h-8 rounded-[4px] bg-field px-2.5 text-[11px] font-medium shadow-hairline hover:bg-hover-2 disabled:opacity-40">Connect another repository</button>

    <div className="mt-4">
      <div className="mb-1.5 flex items-center gap-2 text-[11px] text-ink-3">
        <span className="relative flex size-2" aria-hidden><span className="absolute inline-flex size-full animate-ping rounded-full bg-accent opacity-50" /><span className="relative inline-flex size-2 rounded-full bg-accent" /></span>
        Listening for connected projects…
      </div>
      {connections.length === 0
        ? <div className="rounded-[8px] border border-line bg-inset px-3 py-4 text-center text-[11.5px] text-ink-3">No repositories connected yet.</div>
        : <ul className="overflow-hidden rounded-[8px] border border-line">
          {connections.map((connection) => <li key={connection.project_id} className="flex items-center gap-2 border-b border-line bg-canvas px-3 py-2 text-[12px] last:border-0">
            <span className="size-1.5 shrink-0 rounded-full bg-green" aria-hidden />
            <span className="min-w-0 flex-1 truncate font-medium">{connection.display_name}</span>
            <span className="truncate font-mono text-[10.5px] text-ink-3 max-sm:hidden">{connection.remote}</span>
            <span className="shrink-0 text-[10.5px] text-ink-3">{connection.machine_name}</span>
          </li>)}
        </ul>}
    </div>

    {advance.error && <p role="alert" className="mt-3 text-[11px] text-red">{advance.error.message}</p>}
    <div className="mt-4 flex items-center justify-end gap-2">
      <button type="button" disabled={advance.isPending} onClick={() => advance.mutate()} className="h-9 rounded-[4px] px-3 text-[11.5px] font-medium text-ink-3 hover:bg-hover hover:text-ink disabled:opacity-40">Skip for now</button>
      <button type="button" disabled={advance.isPending} onClick={() => advance.mutate()} className="h-9 rounded-[4px] bg-accent px-3 text-[11.5px] font-medium text-page disabled:opacity-50">{advance.isPending ? 'Continuing…' : 'Continue'}</button>
    </div>
  </OnboardingLayout>
}
