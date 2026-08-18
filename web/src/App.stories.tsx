import type { Meta, StoryObj } from '@storybook/react-vite'
import App from './App'

const meta = { component: App, title: 'Product/Re_gent Explorer', tags: ['ai-generated'], parameters: { layout: 'fullscreen' } } satisfies Meta<typeof App>
export default meta
type Story = StoryObj<typeof meta>

export const ConversationsIndex: Story = {}
