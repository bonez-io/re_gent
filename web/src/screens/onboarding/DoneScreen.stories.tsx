import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { useState, type PropsWithChildren } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { expect, userEvent, waitFor } from 'storybook/test'
import { api } from '../../api/client'
import { DoneScreen } from './DoneScreen'

const org = { slug: 'acme', display_name: 'Acme Corp', server_url: 'http://127.0.0.1:8081', onboarding: 'done' }
const me = { user: { id: 'usr_admin', username: 'admin', display_name: 'Ada Admin' }, orgs: [{ slug: 'acme', display_name: 'Acme Corp', role: 'admin', onboarding: 'done' }] }

function SelfHosted({ children }: PropsWithChildren) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={['/setup/done']}><Routes><Route path="/setup/done" element={children} /></Routes></MemoryRouter></QueryClientProvider>
}

// Mounts the same ['capabilities']/['auth-me'] queries the top-level AuthGate keeps active
// for the life of the app, so that DoneScreen's invalidateQueries on "Go to re_gent" causes
// a real refetch we can count — invalidating an *inactive* query would be a silent no-op.
function CapabilitiesAndAuthMeProbe() {
  useQuery({ queryKey: ['capabilities'], queryFn: api.capabilities, retry: false })
  useQuery({ queryKey: ['auth-me'], queryFn: api.me, retry: false })
  return null
}

function BounceHarness({ children }: PropsWithChildren) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={['/setup/done']}>
    <CapabilitiesAndAuthMeProbe />
    <Routes>
      <Route path="/setup/done" element={children} />
      <Route path="/" element={<div>Repo home reached</div>} />
    </Routes>
  </MemoryRouter></QueryClientProvider>
}

function Managed({ children }: PropsWithChildren) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={['/o/acme/setup/done']}><Routes><Route path="/o/:slug/setup/done" element={children} /></Routes></MemoryRouter></QueryClientProvider>
}

const meta = { component: DoneScreen, tags: ['ai-generated'], parameters: { layout: 'fullscreen' } } satisfies Meta<typeof DoneScreen>
export default meta
type Story = StoryObj<typeof meta>

export const SelfHosted_: Story = {
  render: () => <SelfHosted><DoneScreen /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole('heading', { name: "You're set up" })).toBeVisible()
    await expect(canvas.getByText(/Acme Corp is ready/)).toBeVisible()
    await expect(canvas.getByText(/rgt auth login http:\/\/127\.0\.0\.1:8081/)).toBeVisible()
  },
}

export const Managed_: Story = {
  render: () => <Managed><DoneScreen /></Managed>,
  beforeEach({ msw }) {
    msw.use(http.get('/api/v1/orgs/:slug', () => HttpResponse.json({ ...org, slug: 'acme', server_url: undefined })))
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole('heading', { name: "You're set up" })).toBeVisible()
    // Managed has no server_url in the org record; the command falls back to this origin.
    await expect(canvas.getByText(/rgt auth login/)).toBeVisible()
  },
}

// GitHub #105: "Go to re_gent" must land on the repo home, not bounce back into the
// wizard — which happens if capabilities/auth-me are stale from before onboarding
// finished. Counted per-story and reset in beforeEach so runs cannot leak into each other.
let capabilitiesCalls = 0
let meCalls = 0

export const GoToRegentBouncesHomeWithFreshIdentity: Story = {
  render: () => <BounceHarness><DoneScreen /></BounceHarness>,
  beforeEach({ msw }) {
    capabilitiesCalls = 0
    meCalls = 0
    msw.use(
      http.get('/api/v1/auth/me', () => { meCalls += 1; return HttpResponse.json(me) }),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      http.get('/api/v1/capabilities', () => {
        capabilitiesCalls += 1
        return HttpResponse.json({ deployment: 'self-hosted', api_version: 'v1', auth_methods: ['password'], auth_starts: {}, features: [] })
      }),
    )
  },
  play: async ({ canvas }) => {
    await waitFor(() => expect(meCalls).toBeGreaterThanOrEqual(1))
    await waitFor(() => expect(capabilitiesCalls).toBeGreaterThanOrEqual(1))
    const meBefore = meCalls
    const capabilitiesBefore = capabilitiesCalls

    await userEvent.click(await canvas.findByRole('button', { name: 'Go to re_gent' }))

    await waitFor(() => expect(canvas.getByText('Repo home reached')).toBeVisible())
    await waitFor(() => expect(meCalls).toBeGreaterThan(meBefore))
    await waitFor(() => expect(capabilitiesCalls).toBeGreaterThan(capabilitiesBefore))
  },
}
