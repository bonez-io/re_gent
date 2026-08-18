import type { Meta, StoryObj } from '@storybook/react-vite'
import { SessionRow } from './SessionRow'

const meta = { component: SessionRow, tags: ['ai-generated'], args: { title: 'Refine reminder scheduling', author: 'Shay Livne', agent: 'Codex', model: 'gpt-5.6', steps: 42, relativeTime: '2m' }, decorators: [(Story) => <div className="w-[680px] max-w-full overflow-hidden rounded-card shadow-hairline"><Story /></div>] } satisfies Meta<typeof SessionRow>
export default meta
type Story = StoryObj<typeof meta>

export const Default: Story = {}
export const Selected: Story = { args: { selected: true } }
export const PartialLegacyData: Story = { args: { author: undefined, agent: undefined, model: undefined } }
export const LongTitle: Story = { args: { title: 'Refactor reminder scheduling while preserving timezone context across every supported natural-language input' } }
