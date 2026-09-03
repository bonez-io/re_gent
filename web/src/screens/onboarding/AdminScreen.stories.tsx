import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { useState, type PropsWithChildren } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { expect, userEvent, waitFor } from 'storybook/test'
import { AdminScreen } from './AdminScreen'

function Providers({ children }: PropsWithChildren) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={['/setup']}>
    <Routes>
      <Route path="/setup" element={children} />
      <Route path="/setup/connect" element={<div>Connect screen reached</div>} />
    </Routes>
  </MemoryRouter></QueryClientProvider>
}

const meta = { component: AdminScreen, tags: ['ai-generated'], decorators: [(Story) => <Providers><Story /></Providers>], parameters: { layout: 'fullscreen' } } satisfies Meta<typeof AdminScreen>
export default meta
type Story = StoryObj<typeof meta>

export const Default: Story = {
  play: async ({ canvas }) => {
    await expect(canvas.getByRole('heading', { name: 'Organization and admin' })).toBeVisible()
    await expect(canvas.getByLabelText('Slug')).toHaveValue('')
    await userEvent.type(canvas.getByLabelText('Organization name'), 'Acme Corp')
    await expect(canvas.getByLabelText('Slug')).toHaveValue('acme-corp')
  },
}

/** Editing the slug by hand stops it from tracking further org-name edits. */
export const SlugIsEditable: Story = {
  play: async ({ canvas }) => {
    await userEvent.type(canvas.getByLabelText('Organization name'), 'Acme Corp')
    await userEvent.clear(canvas.getByLabelText('Slug'))
    await userEvent.type(canvas.getByLabelText('Slug'), 'acme')
    await userEvent.type(canvas.getByLabelText('Organization name'), ' International')
    await expect(canvas.getByLabelText('Slug')).toHaveValue('acme')
  },
}

export const SubmitCreatesOrgAndAdvances: Story = {
  beforeEach({ msw }) {
    msw.use(http.post('/api/v1/onboarding/admin', async ({ request }) => {
      const body = await request.json() as { org: { display_name: string; slug: string } }
      return HttpResponse.json({ user: { id: 'usr_admin', username: 'admin', display_name: 'Admin' }, csrf: 'csrf-token', org: { slug: body.org.slug, display_name: body.org.display_name, onboarding: 'connect' } })
    }))
  },
  play: async ({ canvas }) => {
    await userEvent.type(canvas.getByLabelText('Organization name'), 'Acme Corp')
    await userEvent.type(canvas.getByLabelText('Admin display name'), 'Ada Admin')
    await userEvent.type(canvas.getByLabelText('New password'), 'correct horse battery staple')
    await userEvent.click(canvas.getByRole('button', { name: 'Continue' }))
    await waitFor(async () => expect(await canvas.findByText('Connect screen reached')).toBeVisible())
  },
}

export const SubmitError: Story = {
  beforeEach({ msw }) {
    msw.use(http.post('/api/v1/onboarding/admin', () => HttpResponse.json({ error: 'Password too short', code: 'password_too_short' }, { status: 400 })))
  },
  play: async ({ canvas }) => {
    await userEvent.type(canvas.getByLabelText('Organization name'), 'Acme Corp')
    await userEvent.type(canvas.getByLabelText('Admin display name'), 'Ada Admin')
    await userEvent.type(canvas.getByLabelText('New password'), 'short-but-12c')
    await userEvent.click(canvas.getByRole('button', { name: 'Continue' }))
    await expect(await canvas.findByRole('alert')).toHaveTextContent('Password too short')
  },
}
