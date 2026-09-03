import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { OnboardingLayout, OnboardingPending, OnboardingProblem } from './chrome'
import { useOnboardingBase } from './shared'

/** Screen 4: summary and the teammate instructions. */
export function DoneScreen() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { deployment, org, serverUrl } = useOnboardingBase()
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const installCommand = `curl -fsSL ${serverUrl}/install | sh\nrgt auth login ${serverUrl}`
  const copy = async () => { try { await navigator.clipboard.writeText(installCommand); setCopyState('copied') } catch { setCopyState('failed') } }

  if (org.isPending) return <OnboardingPending label="Finishing up…" />
  if (org.error) return <OnboardingProblem error={org.error} onRetry={() => org.refetch()} />

  return <OnboardingLayout deployment={deployment} current="done" title="You're set up" description={`${org.data?.display_name ?? 'Your organization'} is ready. Share these instructions with your team.`}>
    <div className="rounded-[8px] border border-line bg-inset p-3">
      <h2 className="m-0 text-[12px] font-semibold">Teammate instructions</h2>
      <p className="mb-2 mt-1 text-[11px] text-ink-3">Install re_gent, then sign in with your invitation link.</p>
      <div className="flex items-start justify-between gap-2 overflow-hidden rounded-[4px] bg-canvas p-2.5 shadow-hairline">
        <pre tabIndex={0} className="m-0 min-w-0 flex-1 overflow-x-auto whitespace-pre font-mono text-[11px] text-ink-2">{installCommand}</pre>
        <button type="button" onClick={() => void copy()} className="shrink-0 rounded-[4px] bg-field px-2 py-1 text-[10.5px] font-medium shadow-hairline hover:bg-hover-2">{copyState === 'copied' ? 'Copied' : copyState === 'failed' ? 'Copy failed' : 'Copy'}</button>
      </div>
    </div>
    <div className="mt-4 flex flex-wrap items-center gap-3">
      <button type="button" onClick={() => { void Promise.all([queryClient.invalidateQueries({ queryKey: ['capabilities'] }), queryClient.invalidateQueries({ queryKey: ['auth-me'] })]).then(() => navigate('/')) }} className="h-10 rounded-[4px] bg-accent px-4 text-[12px] font-medium text-page">Go to re_gent</button>
      <span className="text-[11px] text-ink-3">Sign-in methods and invitations stay available from Settings any time.</span>
    </div>
  </OnboardingLayout>
}
