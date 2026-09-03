import { useQuery } from '@tanstack/react-query'
import { useParams } from 'react-router-dom'
import { api } from '../../api/client'
import { onboardingApi } from '../../api/onboarding'
import type { Deployment } from './path'

/**
 * Resolves which deployment mounted this wizard, the org slug, and the org record.
 * Self-hosted has no `:slug` in the URL — the slug comes from `GET /api/v1/auth/me`
 * `orgs[0]`, which is empty until screen 1 creates the organization. Managed always
 * carries `:slug` from the route (`o/:slug/setup/*`), and the org already exists by
 * the time that route is reachable.
 */
export function useOnboardingBase() {
  const { slug: paramSlug } = useParams<{ slug?: string }>()
  const deployment: Deployment = paramSlug ? 'managed' : 'self-hosted'
  const me = useQuery({ queryKey: ['onboarding-me'], queryFn: api.me, enabled: deployment === 'self-hosted', retry: false })
  const slug = deployment === 'managed' ? paramSlug : me.data?.orgs[0]?.slug
  const org = useQuery({ queryKey: ['onboarding-org', slug], queryFn: () => onboardingApi.org(slug!), enabled: Boolean(slug), retry: false })
  const serverUrl = (deployment === 'managed' ? window.location.origin : org.data?.server_url || window.location.origin).replace(/\/+$/, '')
  return { deployment, slug, me, org, serverUrl }
}
