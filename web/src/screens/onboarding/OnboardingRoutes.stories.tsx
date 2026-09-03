import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { useState, type PropsWithChildren } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { expect, waitFor } from 'storybook/test'
import { OnboardingRoutes } from './OnboardingRoutes'

function SelfHosted({ children, path = '/setup' }: PropsWithChildren<{ path?: string }>) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><Routes><Route path="/setup/*" element={children} /></Routes></MemoryRouter></QueryClientProvider>
}

function Managed({ children, path = '/o/acme/setup' }: PropsWithChildren<{ path?: string }>) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><Routes><Route path="/o/:slug/setup/*" element={children} /></Routes></MemoryRouter></QueryClientProvider>
}

const meta = { component: OnboardingRoutes, tags: ['ai-generated'], parameters: { layout: 'fullscreen' } } satisfies Meta<typeof OnboardingRoutes>
export default meta
type Story = StoryObj<typeof meta>

/** A bare `/setup` visit before any organization exists lands directly on screen 1. */
export const IndexShowsAdminScreenBeforeOrgExists: Story = {
  render: () => <SelfHosted><OnboardingRoutes /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(http.get('/api/v1/auth/me', () => HttpResponse.json({ user: { id: 'usr_admin', username: 'admin', display_name: 'Admin' }, orgs: [] })))
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole('heading', { name: 'Organization and admin' })).toBeVisible()
  },
}

/** A bare `/setup` visit resumes at the screen matching the org's current state. */
export const IndexResumesAtUsersScreen: Story = {
  render: () => <SelfHosted><OnboardingRoutes /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json({ user: { id: 'usr_admin', username: 'admin', display_name: 'Admin' }, orgs: [{ slug: 'acme', display_name: 'Acme Corp', role: 'admin', onboarding: 'users' }] })),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json({ slug: 'acme', display_name: 'Acme Corp', server_url: 'http://127.0.0.1:8081', onboarding: 'users' })),
      http.get('/api/v1/orgs/:slug/auth-methods', () => HttpResponse.json({ password: { enabled: true }, github: { enabled: false }, google: { enabled: false }, smtp: { enabled: false } })),
      http.get('/api/v1/orgs/:slug/connections', () => HttpResponse.json({ connections: [], cursor: 'c1' })),
      http.get('/api/v1/orgs/:slug/invitations', () => HttpResponse.json([])),
    )
  },
  play: async ({ canvas }) => {
    await waitFor(async () => expect(await canvas.findByRole('heading', { name: 'Users' })).toBeVisible())
  },
}

/** Managed mounts at `o/:slug/setup/*` and resumes the same way, reading the slug from the URL. */
export const ManagedIndexResumesAtDone: Story = {
  render: () => <Managed><OnboardingRoutes /></Managed>,
  beforeEach({ msw }) {
    msw.use(http.get('/api/v1/orgs/:slug', () => HttpResponse.json({ slug: 'acme', display_name: 'Acme Corp', onboarding: 'done' })))
  },
  play: async ({ canvas }) => {
    await waitFor(async () => expect(await canvas.findByRole('heading', { name: "You're set up" })).toBeVisible())
  },
}
