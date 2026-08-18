import type { Meta, StoryObj } from '@storybook/react-vite'
import { conversations } from '../mocks/regent'
import { SessionRow } from './SessionRow'

const meta = { component: SessionRow, tags: ['ai-generated'], args: conversations[0], decorators: [(Story) => <div className="w-[760px] max-w-full overflow-hidden border-x border-t border-line"><Story /></div>] } satisfies Meta<typeof SessionRow>
export default meta
type Story = StoryObj<typeof meta>

export const CodexCapturing: Story = {}
export const Selected: Story = { args: { selected: true } }
export const ClaudeCode: Story = { args: conversations[1] }
export const OpenCode: Story = { args: conversations[3] }
export const PartialLegacyData: Story = { args: conversations[5] }
export const LongTitle: Story = { args: { ...conversations[0], title: 'Refactor reminder scheduling while preserving timezone context across every supported natural-language input' } }
