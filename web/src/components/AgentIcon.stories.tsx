import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect } from 'storybook/test'
import { AgentIcon } from './AgentIcon'

const meta = { component: AgentIcon, tags: ['ai-generated'], args: { origin: 'claude_code' }, argTypes: { color: { control: 'boolean' } }, render: (args) => <span className="text-2xl text-ink"><AgentIcon {...args} /></span> } satisfies Meta<typeof AgentIcon>
export default meta
type Story = StoryObj<typeof meta>

export const ClaudeCode: Story = {}
export const Codex: Story = { args: { origin: 'codex' } }
export const OpenCode: Story = { args: { origin: 'opencode' } }
export const Pi: Story = { args: { origin: 'pi' } }
/** Unrecognized origins fall back to a neutral mark instead of crashing. */
export const UnknownOrigin: Story = { args: { origin: 'some_agent' } }
export const NoOrigin: Story = { args: { origin: undefined } }
export const ColorVariant: Story = { args: { origin: 'codex', color: true } }
/** Adjacent visible text already names the vendor, so the icon stays out of the a11y tree. */
export const Decorative: Story = { args: { origin: 'claude_code', decorative: true } }

export const LabelledHasAccessibleName: Story = {
  args: { origin: 'claude_code' },
  play: async ({ canvas }) => {
    await expect(canvas.getByRole('img', { name: 'Claude Code' })).toBeVisible()
  },
}
export const DecorativeHasNoAccessibleName: Story = {
  args: { origin: 'claude_code', decorative: true },
  play: async ({ canvas }) => {
    await expect(canvas.queryByRole('img')).toBeNull()
  },
}
