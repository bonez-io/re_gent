import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { useState, type PropsWithChildren } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { expect, userEvent, waitFor } from 'storybook/test'
import { ConnectScreen } from './ConnectScreen'

const org = { slug: 'acme', display_name: 'Acme Corp', server_url: 'http://127.0.0.1:8081', join_policy: 'invite_only', default_role: 'reader', onboarding: 'connect' }
const me = { user: { id: 'usr_admin', username: 'admin', display_name: 'Ada Admin' }, orgs: [{ slug: 'acme', display_name: 'Acme Corp', role: 'admin', onboarding: 'connect' }] }

function SelfHosted({ children }: PropsWithChildren) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={['/setup/connect']}>
    <Routes>
      <Route path="/setup/connect" element={children} />
      <Route path="/setup/users" element={<div>Users screen reached</div>} />
    </Routes>
  </MemoryRouter></QueryClientProvider>
}

function Managed({ children }: PropsWithChildren) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={['/o/acme/setup/connect']}>
    <Routes>
      <Route path="/o/:slug/setup/connect" element={children} />
      <Route path="/o/:slug/setup/users" element={<div>Users screen reached</div>} />
    </Routes>
  </MemoryRouter></QueryClientProvider>
}

const meta = { component: ConnectScreen, tags: ['ai-generated'], parameters: { layout: 'fullscreen' } } satisfies Meta<typeof ConnectScreen>
export default meta
type Story = StoryObj<typeof meta>

// Every story needs its own connections-feed closure so poll counters do not leak
// between stories; msw.use only ever adds handlers for the story currently running.
function connectionsFeed(rowAfter: { project_id: string; display_name: string; remote: string; machine_name: string; connected_by: string; connected_at: string } | undefined, delayMs = 500) {
  let calls = 0
  return http.get('/api/v1/orgs/:slug/connections', async ({ request }) => {
    const cursor = new URL(request.url).searchParams.get('cursor')
    calls += 1
    if (!cursor) return HttpResponse.json({ connections: [], cursor: 'c1' })
    if (calls === 2 && rowAfter) {
      await new Promise((resolve) => setTimeout(resolve, delayMs))
      return HttpResponse.json({ connections: [rowAfter], cursor: 'c2' })
    }
    // Later polls hang like the real 25s long-poll would; the story's assertions finish
    // long before this matters, and unmount aborts it.
    return new Promise(() => {})
  })
}

const sampleConnection = { project_id: 'proj_1', display_name: 'girlfriend-assistant', remote: 'git@github.com:acme/girlfriend-assistant.git', machine_name: 'shay-mbp', connected_by: 'ada', connected_at: new Date().toISOString() }

export const SelfHostedEmpty: Story = {
  render: () => <SelfHosted><ConnectScreen /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      http.post('/api/v1/orgs/:slug/setup-codes', () => HttpResponse.json({ code: 'SETUP1', expires_at: new Date(Date.now() + 900_000).toISOString(), command: 'curl -fsSL http://127.0.0.1:8081/install | sh && rgt connect http://127.0.0.1:8081 --setup SETUP1' })),
      connectionsFeed(undefined),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByText(/rgt connect .* --setup SETUP1/)).toBeVisible()
    await expect(canvas.getByText('No repositories connected yet.')).toBeVisible()
  },
}

/** A connected repository appears in the live feed without a page reload. */
export const ConnectionArrivesLive: Story = {
  render: () => <SelfHosted><ConnectScreen /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      http.post('/api/v1/orgs/:slug/setup-codes', () => HttpResponse.json({ code: 'SETUP1', expires_at: new Date(Date.now() + 900_000).toISOString(), command: 'curl -fsSL http://127.0.0.1:8081/install | sh && rgt connect http://127.0.0.1:8081 --setup SETUP1' })),
      connectionsFeed(sampleConnection, 500),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByText('No repositories connected yet.')).toBeVisible()
    await waitFor(() => expect(canvas.getByText('girlfriend-assistant')).toBeVisible(), { timeout: 3000 })
    await expect(canvas.getByText('shay-mbp')).toBeVisible()
  },
}

export const ManagedWithConnection: Story = {
  render: () => <Managed><ConnectScreen /></Managed>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json({ ...org, slug: 'acme' })),
      http.post('/api/v1/orgs/:slug/setup-codes', () => HttpResponse.json({ code: 'SETUP2', expires_at: new Date(Date.now() + 900_000).toISOString(), command: 'curl -fsSL https://app.regent.dev/install | sh && rgt connect https://app.regent.dev --setup SETUP2' })),
      connectionsFeed(sampleConnection, 300),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByText(/--setup SETUP2/)).toBeVisible()
    await waitFor(() => expect(canvas.getByText('girlfriend-assistant')).toBeVisible(), { timeout: 3000 })
  },
}

export const ContinueAdvancesToUsers: Story = {
  render: () => <SelfHosted><ConnectScreen /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      http.post('/api/v1/orgs/:slug/setup-codes', () => HttpResponse.json({ code: 'SETUP1', expires_at: new Date(Date.now() + 900_000).toISOString(), command: 'curl -fsSL http://127.0.0.1:8081/install | sh && rgt connect http://127.0.0.1:8081 --setup SETUP1' })),
      connectionsFeed(undefined),
      http.post('/api/v1/orgs/:slug/onboarding', () => HttpResponse.json({ ...org, onboarding: 'users' })),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole('button', { name: 'Continue' })).toBeEnabled()
    await userEvent.click(canvas.getByRole('button', { name: 'Continue' }))
    await waitFor(async () => expect(await canvas.findByText('Users screen reached')).toBeVisible())
  },
}

/** "Connect another repository" mints a fresh setup code while keeping the existing list. */
export const ConnectAnotherMintsNewCode: Story = {
  render: () => <SelfHosted><ConnectScreen /></SelfHosted>,
  beforeEach({ msw }) {
    let mintCount = 0
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      http.post('/api/v1/orgs/:slug/setup-codes', () => { mintCount += 1; return HttpResponse.json({ code: `SETUP${mintCount}`, expires_at: new Date(Date.now() + 900_000).toISOString(), command: `curl -fsSL http://127.0.0.1:8081/install | sh && rgt connect http://127.0.0.1:8081 --setup SETUP${mintCount}` }) }),
      connectionsFeed(sampleConnection, 300),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByText(/--setup SETUP1/)).toBeVisible()
    await waitFor(() => expect(canvas.getByText('girlfriend-assistant')).toBeVisible(), { timeout: 3000 })
    await userEvent.click(canvas.getByRole('button', { name: 'Connect another repository' }))
    await expect(await canvas.findByText(/--setup SETUP2/)).toBeVisible()
    // The previously connected repository is still listed.
    await expect(canvas.getByText('girlfriend-assistant')).toBeVisible()
  },
}
