// Deliberately loose (`onboarding?: string`, `deployment: string`) to match the exact
// signature U1 mounts against — callers may pass a plain org/capabilities response
// without narrowing to the stricter OnboardingState/deployment unions first.
export type Deployment = 'self-hosted' | 'managed'

const segmentFor: Record<string, string> = {
  admin_password: '',
  connect: '/connect',
  users: '/users',
  done: '/done',
}

export function onboardingPathFor(org: { slug: string; onboarding?: string }, deployment: string): string {
  const base = deployment === 'managed' ? `/o/${encodeURIComponent(org.slug)}/setup` : '/setup'
  const segment = org.onboarding !== undefined ? segmentFor[org.onboarding] : undefined
  return `${base}${segment ?? '/done'}`
}
