import type { Meta, StoryObj } from '@storybook/react-vite'
import App from './App'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { useState, type PropsWithChildren } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { expect, fn } from 'storybook/test'

function AppProviders({ children, initialPath }: PropsWithChildren<{ initialPath?: string }>) {
  const [queryClient] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={queryClient}><MemoryRouter initialEntries={[initialPath || '/']}>{children}</MemoryRouter></QueryClientProvider>
}

const meta = {
  component: App,
  title: 'Product/Re_gent Explorer',
  tags: ['ai-generated'],
  decorators: [(Story, context) => <AppProviders key={context.id} initialPath={(context.parameters as { initialPath?: string }).initialPath}><Story /></AppProviders>],
  parameters: { layout: 'fullscreen' },
  beforeEach({ msw }) {
    // RepoHome always asks for the named-project list first; a bare 404 here falls back
    // to the legacy /repos list the base handlers already provide, so every story that
    // doesn't care about sign-in still lands on the sessions screen as before.
    msw.use(http.get('/api/v1/projects', () => new HttpResponse(null, { status: 404 })))
  },
} satisfies Meta<typeof App>
export default meta
type Story = StoryObj<typeof meta>

export const Sessions: Story = {}

const writeClipboard = fn(async () => undefined)

export const EmptyServer: Story = {
  beforeEach({ msw }) {
    msw.use(http.get('/repos', () => HttpResponse.json({ repos: [] })))
    const clipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: writeClipboard } })
    return () => {
      if (clipboard) Object.defineProperty(navigator, 'clipboard', clipboard)
      else Reflect.deleteProperty(navigator, 'clipboard')
    }
  },
  play: async ({ canvas, userEvent }) => {
    await expect(await canvas.findByRole('heading', { name: 'Connect a project' })).toBeVisible()
    await expect(canvas.queryByRole('textbox')).not.toBeInTheDocument()
    await expect(canvas.getByRole('status')).toHaveTextContent('Listening for a connected project')

    const copy = canvas.getByRole('button', { name: 'Copy connect command' })
    await userEvent.click(copy)
    await expect(copy).toHaveTextContent('Copied')
    await expect(writeClipboard).toHaveBeenCalledWith('rgt connect http://127.0.0.1:7654')
  },
}

// --- Sign-in ---

export const ManagedSignIn: Story = {
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/capabilities', () => HttpResponse.json({
        deployment: 'managed',
        api_version: 'v1',
        auth_methods: ['dev', 'device', 'browser_session', 'github', 'google'],
        auth_starts: { dev: '/api/v1/auth/dev/start', github: '/api/v1/auth/github/start', google: '/api/v1/auth/google/start' },
        features: [],
      })),
      http.get('/api/v1/auth/me', () => new HttpResponse(null, { status: 401 })),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole('heading', { name: 'Sign in to re_gent' })).toBeVisible()
    await expect(canvas.getByRole('link', { name: 'Continue with GitHub' })).toHaveAttribute('href', expect.stringContaining('/api/v1/auth/github/start'))
    await expect(canvas.getByRole('link', { name: 'Continue with Google' })).toHaveAttribute('href', expect.stringContaining('/api/v1/auth/google/start'))
    await expect(canvas.getByLabelText('Dev sign-in')).toBeVisible()
    await expect(canvas.queryByLabelText('Personal access token')).not.toBeInTheDocument()
    await expect(canvas.queryByLabelText('Password')).not.toBeInTheDocument()
  },
}

export const SelfHostedPasswordSignIn: Story = {
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/capabilities', () => HttpResponse.json({ deployment: 'self-hosted', api_version: 'v1', auth_methods: ['password', 'browser_session'], auth_starts: {}, features: [] })),
      http.get('/api/v1/auth/me', () => new HttpResponse(null, { status: 401 })),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole('heading', { name: 'Sign in to re_gent' })).toBeVisible()
    await expect(canvas.getByLabelText('Username')).toBeVisible()
    await expect(canvas.getByLabelText('Password')).toHaveAttribute('type', 'password')
    await expect(canvas.queryByLabelText('Personal access token')).not.toBeInTheDocument()
    await expect(canvas.queryByRole('link')).not.toBeInTheDocument()
  },
}

export const LegacyTokenOnly: Story = {
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/capabilities', () => HttpResponse.json({ deployment: 'self-hosted', api_version: 'v1', auth_methods: ['pat', 'browser_session'], auth_starts: {}, features: [] })),
      http.get('/api/v1/auth/me', () => new HttpResponse(null, { status: 401 })),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole('heading', { name: 'Sign in to re_gent' })).toBeVisible()
    await expect(canvas.getByLabelText('Personal access token')).toHaveAttribute('type', 'password')
  },
}

export const PasswordChangeRequiredRedirect: Story = {
  beforeEach({ msw }) {
    let signedIn = false
    msw.use(
      http.get('/api/v1/capabilities', () => HttpResponse.json({ deployment: 'self-hosted', api_version: 'v1', auth_methods: ['password', 'browser_session'], auth_starts: {}, onboarding: 'admin_password', features: [] })),
      http.get('/api/v1/auth/me', () => signedIn
        ? HttpResponse.json({ user: { id: 'u1', username: 'admin', display_name: 'Admin' }, orgs: [], csrf_token: 'csrf-1' })
        : new HttpResponse(null, { status: 401 })),
      http.post('/api/v1/auth/login', () => {
        signedIn = true
        return HttpResponse.json({ user: { id: 'u1', username: 'admin', display_name: 'Admin' }, csrf: 'csrf-1', password_change_required: true }, { status: 201 })
      }),
    )
  },
  play: async ({ canvas, userEvent }) => {
    await expect(await canvas.findByRole('heading', { name: 'Sign in to re_gent' })).toBeVisible()
    await userEvent.type(canvas.getByLabelText('Username'), 'admin')
    await userEvent.type(canvas.getByLabelText('Password'), 'the-initial-password')
    await userEvent.click(canvas.getByRole('button', { name: 'Sign in' }))
    // The wizard screen itself belongs to stream U2; here we only prove the sign-in
    // screen for the old, about-to-be-revoked password is gone.
    await expect(canvas.queryByRole('heading', { name: 'Sign in to re_gent' })).not.toBeInTheDocument()
  },
}

// --- Organization gate ---

export const NoOrgCreateScreen: Story = {
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/capabilities', () => HttpResponse.json({ deployment: 'managed', api_version: 'v1', auth_methods: ['dev', 'browser_session'], auth_starts: { dev: '/api/v1/auth/dev/start' }, features: [] })),
      http.get('/api/v1/auth/me', () => HttpResponse.json({ user: { id: 'u1', display_name: 'Ada Lovelace', email: 'ada@example.com' }, orgs: [], csrf_token: 'csrf-1' })),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole('heading', { name: 'Create an organization' })).toBeVisible()
    await expect(canvas.getByLabelText('Display name')).toBeVisible()
    await expect(canvas.getByLabelText('Slug')).toBeVisible()
  },
}

// --- Invitations ---

export const InvitationPagePassword: Story = {
  parameters: { initialPath: '/invitations/token-abc' },
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/capabilities', () => HttpResponse.json({ deployment: 'self-hosted', api_version: 'v1', auth_methods: ['password'], auth_starts: {}, features: [] })),
      // The invitation route bypasses AuthGate's sign-in requirement, but AuthGate still
      // fires its own /auth/me check in the background; give it a quiet 401 to answer.
      http.get('/api/v1/auth/me', () => new HttpResponse(null, { status: 401 })),
      http.get('/api/v1/invitations/token-abc', () => HttpResponse.json({ org_display_name: 'Acme Robotics', email: 'sam@example.com', methods: ['password'] })),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole('heading', { name: 'Join Acme Robotics' })).toBeVisible()
    await expect(canvas.getByText('For sam@example.com')).toBeVisible()
    await expect(canvas.getByLabelText('Display name')).toBeVisible()
    await expect(canvas.getByLabelText('Username')).toBeVisible()
    await expect(canvas.getByLabelText('Password')).toHaveAttribute('type', 'password')
    await expect(canvas.queryByRole('link')).not.toBeInTheDocument()
  },
}

export const InvitationPageProvider: Story = {
  parameters: { initialPath: '/invitations/token-xyz' },
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/capabilities', () => HttpResponse.json({ deployment: 'managed', api_version: 'v1', auth_methods: ['github', 'browser_session'], auth_starts: { github: '/api/v1/auth/github/start' }, features: [] })),
      http.get('/api/v1/auth/me', () => new HttpResponse(null, { status: 401 })),
      http.get('/api/v1/invitations/token-xyz', () => HttpResponse.json({ org_display_name: 'Acme Robotics', username: 'sam', methods: ['github'] })),
    )
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole('heading', { name: 'Join Acme Robotics' })).toBeVisible()
    await expect(canvas.getByText('For @sam')).toBeVisible()
    const link = canvas.getByRole('link', { name: 'Continue with GitHub' })
    await expect(link).toHaveAttribute('href', expect.stringContaining('invite=token-xyz'))
    await expect(canvas.queryByLabelText('Password')).not.toBeInTheDocument()
  },
}

export const InvitationExpired: Story = {
  parameters: { initialPath: '/invitations/token-old' },
  beforeEach({ msw }) {
    msw.use(http.get('/api/v1/invitations/token-old', () => HttpResponse.json({ error: 'invitation expired', code: 'invitation_expired' }, { status: 410 })))
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByText('This invitation link has expired.')).toBeVisible()
  },
}

// --- Not invited ---

export const NotInvited: Story = {
  parameters: { initialPath: '/not-invited?reason=No%20membership%20on%20file' },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole('heading', { name: 'Your account is not invited' })).toBeVisible()
    await expect(canvas.getByText(/No membership on file/)).toBeVisible()
  },
}

// --- Device approval ---

const deviceMe = { user: { id: 'u1', display_name: 'Ada Lovelace' }, orgs: [{ slug: 'acme', display_name: 'Acme', role: 'admin', onboarding: 'done' }], csrf_token: 'csrf-1' }
const deviceCapabilities = { deployment: 'managed' as const, api_version: 'v1', auth_methods: ['dev', 'device', 'browser_session'], auth_starts: { dev: '/api/v1/auth/dev/start' }, features: [] }

export const DeviceApprovalSuccess: Story = {
  parameters: { initialPath: '/device?code=WORD-WORD' },
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/capabilities', () => HttpResponse.json(deviceCapabilities)),
      http.get('/api/v1/auth/me', () => HttpResponse.json(deviceMe)),
      http.post('/api/v1/auth/device/approve', () => new HttpResponse(null, { status: 204 })),
    )
  },
  play: async ({ canvas, userEvent }) => {
    await expect(await canvas.findByRole('heading', { name: 'Approve this device' })).toBeVisible()
    await expect(canvas.getByLabelText('Device code')).toHaveValue('WORD-WORD')
    await userEvent.click(canvas.getByRole('button', { name: 'Approve device' }))
    await expect(await canvas.findByText('Device approved. You can return to it now.')).toBeVisible()
  },
}

export const DeviceApprovalFailure: Story = {
  parameters: { initialPath: '/device?code=STALE-CODE' },
  beforeEach({ msw }) {
    msw.use(
      http.get('/api/v1/capabilities', () => HttpResponse.json(deviceCapabilities)),
      http.get('/api/v1/auth/me', () => HttpResponse.json(deviceMe)),
      http.post('/api/v1/auth/device/approve', () => HttpResponse.json({ error: 'device code not found', code: 'not_found' }, { status: 404 })),
    )
  },
  play: async ({ canvas, userEvent }) => {
    await expect(await canvas.findByRole('heading', { name: 'Approve this device' })).toBeVisible()
    await userEvent.click(canvas.getByRole('button', { name: 'Approve device' }))
    await expect(await canvas.findByText('That code was not recognized.')).toBeVisible()
  },
}
