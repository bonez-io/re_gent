import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useLocation, useNavigate } from 'react-router-dom'
import { api } from '../../api/client'
import type { FeedStep } from '../../api/types'
import { OnboardingLayout, OnboardingPending, OnboardingProblem } from './chrome'
import { dismissSetupGate, onboardingPathFor } from './path'
import { useOnboardingBase } from './shared'

const HELLO_FILE = /(^|\/)hello_world\.py$/i
const TEST_FILE = /(^|\/)(test_hello[\w.-]*\.py|hello[\w-]*_test\.py|tests\/.*hello.*\.py)$/i
const isHelloWorldPath = (path: string) => HELLO_FILE.test(path)
const isTestPath = (path: string) => TEST_FILE.test(path)

type TutorialStatus = 'skipped' | 'completed'
const storageKey = (projectId: string) => `regent:tutorial:${projectId}`
function readTutorialStatus(projectId?: string): TutorialStatus | undefined {
  if (!projectId) return undefined
  try {
    const value = localStorage.getItem(storageKey(projectId))
    return value === 'skipped' || value === 'completed' ? value : undefined
  } catch {
    return undefined
  }
}
function writeTutorialStatus(projectId: string, status: TutorialStatus) {
  try { localStorage.setItem(storageKey(projectId), status) } catch { /* private browsing or storage disabled — the wizard still advances */ }
}

interface Landed { stage1?: FeedStep; stage2?: FeedStep; stage3?: FeedStep }

/**
 * Walks the feed steps in arrival order and finds the first step to satisfy each stage,
 * in sequence: stage 2 only looks at steps after stage 1 landed, stage 3 only at steps
 * after stage 2 landed from a different session.
 */
function computeLanded(steps: FeedStep[]): Landed {
  let stage1: FeedStep | undefined
  let stage2: FeedStep | undefined
  let stage3: FeedStep | undefined
  for (const step of steps) {
    if (!stage1 && step.files.some(isHelloWorldPath)) { stage1 = step; continue }
    if (stage1 && !stage2 && step.files.some(isTestPath)) { stage2 = step; continue }
    if (stage2 && !stage3 && step.session_id !== stage2.session_id && step.files.some((path) => isHelloWorldPath(path) || isTestPath(path))) stage3 = step
  }
  return { stage1, stage2, stage3 }
}

const REDIRECT_DELAY_MS = 1200
const POLL_TIMEOUT_SECONDS = 20
const ERROR_BACKOFF_MS = 3000

/**
 * Screen inserted between Connect and Users: a skippable guided demo that long-polls the
 * connected repository's feed for three prompts landing, then auto-redirects into the
 * captured file with its transcript. UI-only — it has no corresponding server onboarding
 * state, so completion/skip is tracked client-side (localStorage, keyed by project id).
 */
export function TutorialScreen() {
  const navigate = useNavigate()
  const location = useLocation()
  const { deployment, slug, org } = useOnboardingBase()
  const queryRepoId = new URLSearchParams(location.search).get('repo') || undefined

  // No repo id carried on the URL: fall back to the first project the server knows about.
  const projects = useQuery({ queryKey: ['onboarding-tutorial-projects'], queryFn: api.listProjects, enabled: !queryRepoId, retry: false })
  const repoId = queryRepoId || projects.data?.projects[0]?.id
  const repoResolved = Boolean(queryRepoId) || !projects.isPending

  const [steps, setSteps] = useState<FeedStep[]>([])
  const [feedError, setFeedError] = useState<Error>()
  const seenHashes = useRef(new Set<string>())
  const { stage1, stage2, stage3 } = useMemo(() => computeLanded(steps), [steps])
  const redirectScheduled = useRef(false)

  const usersPath = slug ? onboardingPathFor({ slug, onboarding: 'users' }, deployment) : undefined
  const targetPath = stage1?.files.find(isHelloWorldPath)

  const goToFile = () => {
    if (!repoId || !stage3 || !targetPath) return
    writeTutorialStatus(repoId, 'completed')
    dismissSetupGate(slug ?? '')
    navigate(`/repos/${encodeURIComponent(repoId)}/files?step=${encodeURIComponent(stage3.hash)}&path=${encodeURIComponent(targetPath)}`)
  }
  const goToFileRef = useRef(goToFile)
  goToFileRef.current = goToFile

  const skip = () => {
    if (repoId) writeTutorialStatus(repoId, 'skipped')
    if (usersPath) navigate(usersPath)
  }

  // Already skipped or completed this tutorial for this project (e.g. the browser back
  // button): resume straight into the normal wizard flow instead of showing it again.
  useEffect(() => {
    if (repoId && usersPath && readTutorialStatus(repoId)) navigate(usersPath, { replace: true })
  }, [repoId, usersPath, navigate])

  // No repository at all to run the demo against: skip it, there is nothing to show.
  useEffect(() => {
    if (repoResolved && !repoId && usersPath) navigate(usersPath, { replace: true })
  }, [repoResolved, repoId, usersPath, navigate])

  // Long-polls the feed for the lifetime of this screen: one call with no `since` gets a
  // starting cursor, then each subsequent call holds the request open until a new step
  // lands or the timeout elapses. Stops on unmount, once stage 3 lands, or when skipped.
  useEffect(() => {
    if (!repoId || stage3 || readTutorialStatus(repoId)) return
    let stopped = false
    const controller = new AbortController()
    const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
    const appendSteps = (incoming: FeedStep[]) => {
      const fresh = incoming.filter((step) => !seenHashes.current.has(step.hash))
      if (!fresh.length) return
      for (const step of fresh) seenHashes.current.add(step.hash)
      setSteps((prev) => [...prev, ...fresh])
    }
    const loop = async () => {
      try {
        const first = await api.feed(repoId, undefined, undefined, controller.signal)
        if (stopped) return
        setFeedError(undefined)
        appendSteps(first.steps)
        let cursor = first.cursor
        while (!stopped) {
          try {
            const response = await api.feed(repoId, cursor, POLL_TIMEOUT_SECONDS, controller.signal)
            if (stopped) return
            setFeedError(undefined)
            cursor = response.cursor
            appendSteps(response.steps)
          } catch (error) {
            if (stopped) return
            setFeedError(error instanceof Error ? error : new Error('Could not reach the feed'))
            await sleep(ERROR_BACKOFF_MS)
          }
        }
      } catch (error) {
        if (!stopped) setFeedError(error instanceof Error ? error : new Error('Could not reach the feed'))
      }
    }
    void loop()
    return () => { stopped = true; controller.abort() }
  }, [repoId, stage3])

  // Stage 3 landed: auto-redirect after a short beat so the completed state is visible,
  // with "Open the file" as the manual fallback if the redirect is somehow blocked.
  useEffect(() => {
    if (!stage3 || redirectScheduled.current) return
    redirectScheduled.current = true
    const timer = setTimeout(() => goToFileRef.current(), REDIRECT_DELAY_MS)
    return () => clearTimeout(timer)
  }, [stage3])

  if (!slug || org.isPending || !repoResolved) return <OnboardingPending label="Loading tutorial…" />
  if (org.error) return <OnboardingProblem error={org.error} onRetry={() => org.refetch()} />
  if (!repoId) return <OnboardingPending label="Loading tutorial…" />

  const stages = [
    { landed: Boolean(stage1), text: 'Open your agent in this repo and ask it to create hello_world.py that prints a greeting.', prompt: stage1?.prompt },
    { landed: Boolean(stage2), text: 'Ask it to write a failing test for hello_world.py.', prompt: stage2?.prompt },
    { landed: Boolean(stage3), text: 'Start a new session and ask it to make the test pass.', prompt: stage3?.prompt },
    { landed: Boolean(stage3), text: stage3 ? `Opening ${targetPath ?? 'the file'} with its transcript…` : 'Then re_gent opens the file, with the transcript that produced each change.', prompt: undefined },
  ]

  return <OnboardingLayout deployment={deployment} current="tutorial" title="See re_gent capture your work" description="Try a real agent turn in the repository you just connected — skip any time.">
    <ol className="grid gap-3">
      {stages.map((stage, index) => <li key={index} className="flex items-start gap-2.5 rounded-[8px] border border-line bg-inset px-3 py-2.5">
        <span aria-hidden className={`mt-1 size-2 shrink-0 rounded-full ${stage.landed ? 'bg-accent' : 'bg-line'}`} />
        <div className="min-w-0 flex-1">
          <p className="m-0 text-[12px] leading-5">{stage.text}</p>
          {stage.prompt && <p className="m-0 mt-1 truncate text-[10.5px] italic text-ink-3" title={stage.prompt}>“{stage.prompt}”</p>}
        </div>
      </li>)}
    </ol>

    {feedError && <p role="alert" className="mt-3 text-[11px] text-red">{feedError.message}</p>}

    <div className="mt-4 flex items-center justify-end gap-2">
      <button type="button" onClick={skip} className="h-9 rounded-[4px] px-3 text-[11.5px] font-medium text-ink-3 hover:bg-hover hover:text-ink">Skip tutorial</button>
      {stage3 && <button type="button" onClick={goToFile} className="h-9 rounded-[4px] bg-accent px-3 text-[11.5px] font-medium text-page">Open the file</button>}
    </div>
  </OnboardingLayout>
}
