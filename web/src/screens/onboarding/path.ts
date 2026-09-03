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

function baseFor(org: { slug: string }, deployment: string): string {
  return deployment === 'managed' ? `/o/${encodeURIComponent(org.slug)}/setup` : '/setup'
}

export function onboardingPathFor(org: { slug: string; onboarding?: string }, deployment: string): string {
  const base = baseFor(org, deployment)
  // An organization that does not report a state is a new one: it has
  // nothing connected yet, so the wizard starts at "connect", never at "done".
  const segment = org.onboarding !== undefined ? segmentFor[org.onboarding] : undefined
  return `${base}${segment ?? '/connect'}`
}

// The guided tutorial is UI-only — it has no corresponding server onboarding state, so it
// gets its own path helper rather than an entry in segmentFor (which mirrors org.onboarding).
export function tutorialPathFor(org: { slug: string }, deployment: string): string {
  return `${baseFor(org, deployment)}/tutorial`
}

// Leaving the wizard on purpose (the tutorial's redirect into the captured file, or
// "Go to re_gent") must not bounce the user straight back in while the organization's
// onboarding state is still short of "done". Remembered per org in localStorage; the
// wizard stays reachable by its URL and from Settings.
const setupGateKey = (slug: string) => `regent:setup:dismissed:${slug || 'self-hosted'}`
export function dismissSetupGate(slug: string) {
  try { localStorage.setItem(setupGateKey(slug), '1') } catch { /* storage unavailable: the gate may bounce once more */ }
}
export function setupGateDismissed(slug: string): boolean {
  try { return localStorage.getItem(setupGateKey(slug)) === '1' } catch { return false }
}
