import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { useState, type PropsWithChildren } from 'react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { expect, waitFor } from 'storybook/test'
import type { FeedStep } from '../../api/types'
import { TutorialScreen } from './TutorialScreen'

const org = { slug: 'acme', display_name: 'Acme Corp', server_url: 'http://127.0.0.1:8081', onboarding: 'users' }
const me = { user: { id: 'usr_admin', username: 'admin', display_name: 'Ada Admin' }, orgs: [{ slug: 'acme', display_name: 'Acme Corp', role: 'admin', onboarding: 'users' }] }

function FilesRouteProbe() {
  const location = useLocation()
  return <div>Files screen reached: {location.pathname}{location.search}</div>
}

// Each story gets its own project id on the URL, not just its own feed handler — the
// screen keys its "already skipped/completed" localStorage entry by project id, and the
// browser session (and its localStorage) is shared across stories in the same test run.
function SelfHosted({ children, repoId }: PropsWithChildren<{ repoId: string }>) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={[`/setup/tutorial?repo=${repoId}`]}>
    <Routes>
      <Route path="/setup/tutorial" element={children} />
      <Route path="/setup/users" element={<div>Users screen reached</div>} />
      <Route path="/repos/:repoId/files" element={<FilesRouteProbe />} />
    </Routes>
  </MemoryRouter></QueryClientProvider>
}

const meta = { component: TutorialScreen, tags: ['ai-generated'], parameters: { layout: 'fullscreen' } } satisfies Meta<typeof TutorialScreen>
export default meta
type Story = StoryObj<typeof meta>

const stage1Step: FeedStep = { hash: 'h1'.padEnd(64, '0'), session_id: 'codex:s1', origin: 'codex', turn_id: 't1', timestamp: new Date().toISOString(), files: ['hello_world.py'], prompt: 'Create hello_world.py that prints a friendly greeting.' }
const stage2Step: FeedStep = { hash: 'h2'.padEnd(64, '0'), session_id: 'codex:s1', origin: 'codex', turn_id: 't2', timestamp: new Date().toISOString(), files: ['test_hello_world.py'], prompt: 'Write a failing test for hello_world.py.' }
const stage3Step: FeedStep = { hash: 'h3'.padEnd(64, '0'), session_id: 'claude_code:s2', origin: 'claude_code', turn_id: 't3', timestamp: new Date().toISOString(), files: ['hello_world.py'], prompt: 'Make the failing test pass.' }

// Every story needs its own feed closure so poll counters do not leak between stories.
// The first call (no `since`) only ever establishes a cursor; each batch below is handed
// out on the calls that follow, one per long-poll round trip. Once batches run out the
// handler hangs like the real long-poll would — assertions finish long before that matters,
// and unmount aborts it.
function feed(repoId: string, batches: FeedStep[][]) {
  let call = 0
  return http.get(`/${repoId}/api/feed`, async ({ request }) => {
    const since = new URL(request.url).searchParams.get('since')
    if (!since) return HttpResponse.json({ cursor: 'c0', steps: [] })
    const index = call
    call += 1
    if (index < batches.length) return HttpResponse.json({ cursor: `c${index + 1}`, steps: batches[index] })
    return new Promise(() => {})
  })
}

export const Idle: Story = {
  render: () => <SelfHosted repoId="proj_idle"><TutorialScreen /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      feed('proj_idle', []),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByText(/ask it to create hello_world\.py/)).toBeVisible()
    await expect(canvas.getByRole('button', { name: 'Skip tutorial' })).toBeEnabled()
    await expect(canvas.queryByRole('button', { name: 'Open the file' })).not.toBeInTheDocument()
  },
}

export const Stage1Lit: Story = {
  render: () => <SelfHosted repoId="proj_stage1"><TutorialScreen /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      feed('proj_stage1', [[stage1Step]]),
    )
  },
  play: async ({ canvas }) => {
    await waitFor(() => expect(canvas.getByText(/Create hello_world\.py that prints a friendly greeting\./)).toBeVisible(), { timeout: 3000 })
    await expect(canvas.queryByRole('button', { name: 'Open the file' })).not.toBeInTheDocument()
  },
}

/** All three stages land; the screen shows the redirect target and then, shortly after,
 *  auto-navigates into the Files view pinned at the step that made the test pass. */
export const AllLitWithRedirect: Story = {
  render: () => <SelfHosted repoId="proj_alllit"><TutorialScreen /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      feed('proj_alllit', [[stage1Step], [stage2Step], [stage3Step]]),
    )
  },
  play: async ({ canvas }) => {
    await waitFor(() => expect(canvas.getByText(/Opening hello_world\.py with its transcript/)).toBeVisible(), { timeout: 3000 })
    await expect(canvas.getByRole('button', { name: 'Open the file' })).toBeEnabled()
    await waitFor(() => expect(canvas.getByText(/Files screen reached/)).toBeVisible(), { timeout: 4000 })
    await expect(canvas.getByText(new RegExp(`step=${stage3Step.hash}`))).toBeVisible()
    await expect(canvas.getByText(/path=hello_world\.py/)).toBeVisible()
  },
}

export const FeedErrorState: Story = {
  render: () => <SelfHosted repoId="proj_error"><TutorialScreen /></SelfHosted>,
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/auth/me', () => HttpResponse.json(me)),
      http.get('/api/v1/orgs/:slug', () => HttpResponse.json(org)),
      http.get('/proj_error/api/feed', () => HttpResponse.json({ error: 'feed unavailable' }, { status: 503 })),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole('alert')).toHaveTextContent('feed unavailable')
  },
}
