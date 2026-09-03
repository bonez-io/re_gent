import type { ReactNode } from 'react'
import type { OnboardingState } from '../../api/types'
import type { Deployment } from './path'

// The guided tutorial sits between "connect" and "users" in the progress bar even though
// it has no corresponding server onboarding state (see path.ts's tutorialPathFor).
export type OnboardingStep = OnboardingState | 'tutorial'

const stepsFor: Record<Deployment, Array<{ key: OnboardingStep; label: string }>> = {
  'self-hosted': [
    { key: 'admin_password', label: 'Organization & admin' },
    { key: 'connect', label: 'Connect' },
    { key: 'tutorial', label: 'Tutorial' },
    { key: 'users', label: 'Users' },
    { key: 'done', label: 'Done' },
  ],
  managed: [
    { key: 'connect', label: 'Connect' },
    { key: 'tutorial', label: 'Tutorial' },
    { key: 'users', label: 'Users' },
    { key: 'done', label: 'Done' },
  ],
}

export function OnboardingLayout({ deployment, current, title, description, children, wide }: {
  deployment: Deployment
  current: OnboardingStep
  title: string
  description: string
  children: ReactNode
  wide?: boolean
}) {
  const steps = stepsFor[deployment]
  const index = steps.findIndex((step) => step.key === current)
  return <main className="flex min-h-screen items-start justify-center bg-page p-4 py-10 text-ink">
    <div className={`w-full ${wide ? 'max-w-2xl' : 'max-w-lg'}`}>
      <div className="mb-5 flex flex-wrap items-center justify-between gap-2">
        <span className="regent-kicker">Setup</span>
        <ol className="flex items-center gap-1.5 text-[10.5px] text-ink-3" aria-label="Setup progress">
          {steps.map((step, i) => <li key={step.key} className={`flex items-center gap-1.5 ${i === index ? 'font-medium text-ink' : ''}`}>
            <span className={`flex size-4 shrink-0 items-center justify-center rounded-full text-[9px] shadow-hairline ${i < index ? 'bg-accent text-page' : i === index ? 'text-ink' : 'text-ink-3'}`}>{i < index ? '✓' : i + 1}</span>
            <span className="max-sm:hidden">{step.label}</span>
            {i < steps.length - 1 && <span className="mx-0.5 h-px w-3 bg-line" aria-hidden />}
          </li>)}
        </ol>
      </div>
      <section className="overflow-hidden rounded-[8px] border border-line bg-canvas shadow-raised">
        <header className="border-b border-line px-5 py-4">
          <h1 className="m-0 text-[18px] font-semibold">{title}</h1>
          <p className="mb-0 mt-1 text-[11.5px] leading-5 text-ink-3">{description}</p>
        </header>
        <div className="p-5">{children}</div>
      </section>
    </div>
  </main>
}

export function OnboardingPending({ label = 'Loading…' }: { label?: string }) {
  return <main className="flex min-h-screen items-center justify-center bg-page text-ink"><div className="flex items-center text-[12px] text-ink-3"><span className="mr-2 size-2 animate-pulse rounded-full bg-accent" />{label}</div></main>
}

export function OnboardingProblem({ error, onRetry }: { error: Error; onRetry?: () => void }) {
  return <main className="flex min-h-screen items-center justify-center bg-page p-4 text-ink"><div className="m-auto max-w-sm px-6 py-10 text-center"><div className="mx-auto mb-2 size-2 rounded-full bg-red" /><h2 className="m-0 text-[15px] font-semibold">Could not load setup</h2><p className="mt-1 text-[12px] leading-5 text-ink-3">{error.message}</p>{onRetry && <button type="button" onClick={onRetry} className="mt-3 h-8 rounded-[4px] bg-field px-3 text-[12px] shadow-hairline hover:bg-hover-2">Retry</button>}</div></main>
}
