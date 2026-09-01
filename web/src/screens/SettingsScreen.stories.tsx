import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { useState, type PropsWithChildren } from 'react'
import { expect } from 'storybook/test'
import { SettingsScreen } from './SettingsScreen'

function Providers({ children }: PropsWithChildren) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

const meta = { component: SettingsScreen, tags: ['ai-generated'], decorators: [(Story) => <Providers><Story /></Providers>], parameters: { layout: 'fullscreen' }, args: { section: 'general', repoId: 'girlfriend-assistant' } } satisfies Meta<typeof SettingsScreen>
export default meta
type Story = StoryObj<typeof meta>

export const General: Story = {}
export const Users: Story = {
  args: { section: 'users' },
  beforeEach({ msw }) {
    msw.use(
      http.get('/girlfriend-assistant/api/v1/access/members', () => HttpResponse.json({ members: [
        { id: 'usr_owner', username: 'shay', display_name: 'Shay Livne', instance_owner: true, created_at: new Date().toISOString(), role: 'owner' },
        { id: 'usr_reader', username: 'reader', display_name: 'Read Only', instance_owner: false, created_at: new Date().toISOString(), role: 'reader' },
      ] })),
      http.get('/api/v1/users', () => HttpResponse.json({ users: [
        { id: 'usr_owner', username: 'shay', display_name: 'Shay Livne', instance_owner: true, created_at: new Date().toISOString() },
        { id: 'usr_reader', username: 'reader', display_name: 'Read Only', instance_owner: false, created_at: new Date().toISOString() },
      ] })),
      http.get('/api/v1/auth/tokens', () => HttpResponse.json({ tokens: [{ id: 'tok_1', name: 'initial setup', prefix: 'rgt_pat_abcd1234', created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 30 * 86_400_000).toISOString() }] })),
      http.put('/girlfriend-assistant/api/v1/access/members', () => new HttpResponse(null, { status: 204 })),
      http.post('/api/v1/users', () => HttpResponse.json({ user: { id: 'usr_writer', username: 'writer', display_name: 'Write User', instance_owner: false, created_at: new Date().toISOString() }, initial_token: 'rgt_pat_shown_once' }, { status: 201 })),
    )
  },
  play: async ({ canvas, userEvent }) => {
    await expect(await canvas.findByRole('heading', { name: 'Access' })).toBeVisible()
    await expect(await canvas.findByText('2 members')).toBeVisible()
    await expect(canvas.getByRole('combobox', { name: 'Role for Shay Livne' })).toBeDisabled()
    await userEvent.type(await canvas.findByRole('textbox', { name: 'Username' }), 'writer')
    await userEvent.type(canvas.getByRole('textbox', { name: 'Display name' }), 'Write User')
    await userEvent.click(canvas.getByRole('button', { name: 'Create user' }))
    await expect(await canvas.findByText("Copy Write User's initial token now")).toBeVisible()
    await expect(canvas.getByText('rgt_pat_shown_once')).toBeVisible()
  },
}
export const Data: Story = {
  args: { section: 'data' },
  play: async ({ canvas }) => {
    await expect(canvas.getByRole('heading', { name: 'Data' })).toBeVisible()
    await expect(canvas.getByText('Semantic index')).toBeVisible()
    await expect(canvas.getByText('Not connected')).toBeVisible()
  },
}
