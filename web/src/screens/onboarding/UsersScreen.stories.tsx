import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { useState, type PropsWithChildren } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { expect, userEvent, waitFor } from 'storybook/test'
import { UsersScreen } from './UsersScreen'

const org = { slug: 'acme', display_name: 'Acme Corp', server_url: 'http://127.0.0.1:8081', join_policy: 'invite_only', default_role: 'reader', onboarding: 'users' }
const me = { user: { id: 'usr_admin', username: 'admin', display_name: 'Ada Admin' }, orgs: [{ slug: 'acme', display_name: 'Acme Corp', role: 'admin', onboarding: 'users' }] }
const connectionsSnapshot = { connections: [{ project_id: 'proj_1', display_name: 'girlfriend-assistant', remote: 'git@github.com:acme/girlfriend-assistant.git', machine_name: 'shay-mbp', connected_by: 'ada', connected_at: new Date().toISOString() }], cursor: 'c1' }
const authMethods = {
  password: { enabled: true },
  github: { enabled: false, client_id: '', has_secret: false, callback_url: 'http://127.0.0.1:8081/api/v1/auth/github/callback' },
  google: { enabled: false, client_id: '', has_secret: false, callback_url: 'http://127.0.0.1:8081/api/v1/auth/google/callback' },
  smtp: { enabled: false, host: '', port: 587, username: '', from: '', has_password: false },
}
const invitations = [
  { id: 'inv_1', email: 'teammate@example.com', org_role: 'member', status: 'pending', expires_at: new Date(Date.now() + 6 * 86_400_000).toISOString(), link: 'http://127.0.0.1:8081/invitations/tok_abc' },
  { id: 'inv_2', username: 'olduser', org_role: 'admin', status: 'accepted', expires_at: new Date(Date.now() - 86_400_000).toISOString() },
]

function SelfHosted({ children }: PropsWithChildren) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={['/setup/users']}>
    <Routes>
      <Route path="/setup/users" element={children} />
      <Route path="/setup/done" element={<div>Done screen reached</div>} />
    </Routes>
  </MemoryRouter></QueryClientProvider>
}

function Managed({ children }: PropsWithChildren) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={['/o/acme/setup/users']}>
    <Routes>
      <Route path="/o/:slug/setup/users" element={children} />
      <Route path="/o/:slug/setup/done" element={<div>Done screen reached</div>} />
    </Routes>
  </MemoryRouter></QueryClientProvider>
}

const meta = { component: UsersScreen, tags: ['ai-generated'], parameters: { layout: 'fullscreen' } } satisfies Meta<typeof UsersScreen>
export default meta
type Story = StoryObj<typeof meta>

export const SelfHostedWithInvitations: Story = {
  render: () => <SelfHosted><UsersScreen /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      http.get('/api/v1/orgs/:slug/auth-methods', () => HttpResponse.json(authMethods)),
      http.get('/api/v1/orgs/:slug/connections', () => HttpResponse.json(connectionsSnapshot)),
      http.get('/api/v1/orgs/:slug/invitations', () => HttpResponse.json(invitations)),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByText('Password')).toBeVisible()
    await expect(await canvas.findByText('teammate@example.com')).toBeVisible()
    await expect(canvas.getByText('pending')).toBeVisible()
    await expect(canvas.getByText('olduser')).toBeVisible()
  },
}

/** Enabling GitHub reveals its settings, including the callback URL to paste into the OAuth app. */
export const EnablingGitHubRevealsSettings: Story = {
  render: () => <SelfHosted><UsersScreen /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      http.get('/api/v1/orgs/:slug/auth-methods', () => HttpResponse.json(authMethods)),
      http.get('/api/v1/orgs/:slug/connections', () => HttpResponse.json(connectionsSnapshot)),
      http.get('/api/v1/orgs/:slug/invitations', () => HttpResponse.json([])),
    )
  },
  play: async ({ canvas }) => {
    const githubToggle = await canvas.findByRole('checkbox', { name: 'GitHub' })
    await expect(githubToggle).not.toBeChecked()
    await userEvent.click(githubToggle)
    await expect(canvas.getByPlaceholderText('Client ID')).toBeVisible()
    await expect(canvas.getByText(/github\/callback/)).toBeVisible()
  },
}

/** Creating an invitation shows its one-time link with a copy button. */
export const CreateInvitationShowsLink: Story = {
  render: () => <SelfHosted><UsersScreen /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      http.get('/api/v1/orgs/:slug/auth-methods', () => HttpResponse.json(authMethods)),
      http.get('/api/v1/orgs/:slug/connections', () => HttpResponse.json(connectionsSnapshot)),
      http.get('/api/v1/orgs/:slug/invitations', () => HttpResponse.json([])),
      http.post('/api/v1/orgs/:slug/invitations', () => HttpResponse.json({ id: 'inv_new', link: 'http://127.0.0.1:8081/invitations/tok_new', expires_at: new Date(Date.now() + 7 * 86_400_000).toISOString(), emailed: false })),
    )
  },
  play: async ({ canvas }) => {
    await userEvent.type(await canvas.findByRole('textbox', { name: 'Email' }), 'new-teammate@example.com')
    await userEvent.click(canvas.getByRole('button', { name: 'Invite' }))
    await expect(await canvas.findByText('http://127.0.0.1:8081/invitations/tok_new')).toBeVisible()
  },
}

export const ManagedShowsFixedProviders: Story = {
  render: () => <Managed><UsersScreen /></Managed>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json({ ...org, slug: 'acme' })),
      http.get('/api/v1/orgs/:slug/connections', () => HttpResponse.json(connectionsSnapshot)),
      http.get('/api/v1/orgs/:slug/invitations', () => HttpResponse.json([])),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByText('GitHub')).toBeVisible()
    await expect(canvas.getByText('Google')).toBeVisible()
    await expect(canvas.getAllByText('On · not configurable')).toHaveLength(2)
    await expect(canvas.getByText(/Verified domains come later/)).toBeVisible()
  },
}

export const ContinueAdvancesToDone: Story = {
  render: () => <SelfHosted><UsersScreen /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      http.get('/api/v1/orgs/:slug/auth-methods', () => HttpResponse.json(authMethods)),
      http.get('/api/v1/orgs/:slug/connections', () => HttpResponse.json(connectionsSnapshot)),
      http.get('/api/v1/orgs/:slug/invitations', () => HttpResponse.json(invitations)),
      http.post('/api/v1/orgs/:slug/onboarding', () => HttpResponse.json({ ...org, onboarding: 'done' })),
    )
  },
  play: async ({ canvas }) => {
    await userEvent.click(await canvas.findByRole('button', { name: 'Continue' }))
    await waitFor(async () => expect(await canvas.findByText('Done screen reached')).toBeVisible())
  },
}
