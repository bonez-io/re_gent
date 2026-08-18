import type { Meta, StoryObj } from '@storybook/react-vite'
import App from './App'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { useState, type PropsWithChildren } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { expect, fn } from 'storybook/test'

function AppProviders({ children }: PropsWithChildren) {
  const [queryClient] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }))
  return <QueryClientProvider client={queryClient}><MemoryRouter>{children}</MemoryRouter></QueryClientProvider>
}

const meta = { component: App, title: 'Product/Re_gent Explorer', tags: ['ai-generated'], decorators: [(Story, context) => <AppProviders key={context.id}><Story /></AppProviders>], parameters: { layout: 'fullscreen' } } satisfies Meta<typeof App>
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
