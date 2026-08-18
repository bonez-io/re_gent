import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect } from 'storybook/test'
import { ToolCallGroup } from './ToolCallGroup'

const calls = [
  { id: 'read', tool: 'Read', summary: 'src/reminders/parser.ts', detail: ['Read 184 lines', 'Located parseReminder at line 42'] },
  { id: 'edit', tool: 'Edit', summary: 'src/reminders/parser.ts', detail: ['+ preserve timezone metadata'] },
  { id: 'test', tool: 'Bash', summary: 'pnpm test parser', detail: ['✓ 18 tests passed'] },
]
const files = [{ path: 'parser.ts', additions: 18, deletions: 6 }, { path: 'parser.test.ts', additions: 34, deletions: 0 }]
const meta = { component: ToolCallGroup, tags: ['ai-generated'], args: { calls, files } } satisfies Meta<typeof ToolCallGroup>
export default meta
type Story = StoryObj<typeof meta>

export const Default: Story = {
  play: async ({ canvas, userEvent }) => {
    const edit = canvas.getByRole('button', { name: /Edit.*parser\.ts/i })
    await userEvent.click(edit)
    await expect(edit).toHaveAttribute('aria-expanded', 'true')
  },
}
export const Collapsed: Story = { args: { defaultOpen: false } }
export const Failed: Story = { args: { calls: [...calls, { id: 'failed', tool: 'Bash', summary: 'pnpm test', detail: ['✗ expected 2 reminders'], status: 'failed' }] } }
