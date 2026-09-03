import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { useState, type PropsWithChildren } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { expect } from 'storybook/test'
import { DoneScreen } from './DoneScreen'

const org = { slug: 'acme', display_name: 'Acme Corp', server_url: 'http://127.0.0.1:8081', onboarding: 'done' }
const me = { user: { id: 'usr_admin', username: 'admin', display_name: 'Ada Admin' }, orgs: [{ slug: 'acme', display_name: 'Acme Corp', role: 'admin', onboarding: 'done' }] }

function SelfHosted({ children }: PropsWithChildren) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={['/setup/done']}><Routes><Route path="/setup/done" element={children} /></Routes></MemoryRouter></QueryClientProvider>
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
