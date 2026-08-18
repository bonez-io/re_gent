import type { Meta, StoryObj } from '@storybook/react-vite'
import App from './App'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

const meta = { component: App, title: 'Product/Re_gent Explorer', tags: ['ai-generated'], decorators: [(Story) => <QueryClientProvider client={queryClient}><MemoryRouter><Story /></MemoryRouter></QueryClientProvider>], parameters: { layout: 'fullscreen' } } satisfies Meta<typeof App>
export default meta
type Story = StoryObj<typeof meta>

export const Sessions: Story = {}
