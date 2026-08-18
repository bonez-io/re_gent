import type { Meta, StoryObj } from '@storybook/react-vite'
import { SyncPanel } from './SyncPanel'

const meta = { component: SyncPanel, tags: ['ai-generated'] } satisfies Meta<typeof SyncPanel>
export default meta
type Story = StoryObj<typeof meta>

export const ConnectedSelfHosted: Story = {}
export const BehindRemote: Story = { args: { state: 'stale' } }
export const Offline: Story = { args: { state: 'offline' } }
