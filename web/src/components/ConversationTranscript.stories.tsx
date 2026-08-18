import type { Meta, StoryObj } from '@storybook/react-vite'
import { transcript } from '../mocks/regent'
import { ConversationTranscript } from './ConversationTranscript'

const meta = { component: ConversationTranscript, tags: ['ai-generated'], args: { entries: transcript }, parameters: { layout: 'fullscreen' } } satisfies Meta<typeof ConversationTranscript>
export default meta
type Story = StoryObj<typeof meta>

export const TwoCapturedTurns: Story = {}
export const SingleTurn: Story = { args: { entries: transcript.slice(0, 9) } }
export const ToolFailure: Story = { args: { entries: [{ type: 'user', id: 'f1', at: '14:22:01', content: 'Run the reminder suite and fix the failing boundary case.' }, { type: 'tools', id: 'f2', at: '14:22:06', calls: [{ id: 'failed', tool: 'Bash', summary: 'pnpm test reminders', detail: ['✗ expected 09:00, received 10:00', '1 failed · 66 passed'], status: 'failed' }] }] } }
export const LegacyTranscript: Story = { args: { entries: [{ type: 'user', id: 'l1', at: 'unknown', content: 'Initial onboarding prompt' }, { type: 'assistant', id: 'l2', at: 'unknown', content: 'This conversation predates normalized reasoning and tool capture.' }] } }
