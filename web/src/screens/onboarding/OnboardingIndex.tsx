import { Navigate } from 'react-router-dom'
import { AdminScreen } from './AdminScreen'
import { OnboardingPending, OnboardingProblem } from './chrome'
import { onboardingPathFor } from './path'
import { useOnboardingBase } from './shared'

/**
 * The index route dispatches to whichever screen matches the org's current onboarding
 * state, so a bare `/setup` or `/o/:slug/setup` resumes where the wizard left off.
 */
export function OnboardingIndex() {
  const { deployment, slug, me, org } = useOnboardingBase()

  if (deployment === 'self-hosted') {
    if (me.isPending) return <OnboardingPending label="Checking setup status…" />
    if (me.error) return <OnboardingProblem error={me.error} onRetry={() => me.refetch()} />
    // No organization yet: this instance is still in the admin_password state, before
    // screen 1 has ever been saved.
    if (!slug) return <AdminScreen />
  }

  if (org.isPending) return <OnboardingPending label="Loading organization…" />
  if (org.error) return <OnboardingProblem error={org.error} onRetry={() => org.refetch()} />

  const state = org.data?.onboarding ?? 'done'
  if (state === 'admin_password') return <AdminScreen />
  return <Navigate replace to={onboardingPathFor({ slug: slug!, onboarding: state }, deployment)} />
}
