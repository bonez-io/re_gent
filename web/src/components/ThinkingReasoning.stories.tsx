import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect } from 'storybook/test'
import { ThinkingReasoning } from './ThinkingReasoning'

const lines = ['Read the current parser and its tests.', 'Preserve timezone context until the natural date resolves.', 'Add regression coverage for relative dates.']
const meta = { component: ThinkingReasoning, tags: ['ai-generated'], args: { lines } } satisfies Meta<typeof ThinkingReasoning>
export default meta
type Story = StoryObj<typeof meta>

export const Collapsed: Story = {
  args: { durationSeconds: 12 },
  play: async ({ canvas, userEvent }) => {
    const toggle = canvas.getByRole('button', { name: 'Toggle reasoning' })
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await userEvent.click(toggle)
    await expect(toggle).toHaveAttribute('aria-expanded', 'true')
  },
}
export const Expanded: Story = { args: { defaultOpen: true } }
export const Thinking: Story = { args: { thinking: true } }
