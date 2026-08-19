import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, within } from 'storybook/test'
import { skills } from '../api/skills'
import { SkillDetail } from './SkillDetail'

const meta = {
  component: SkillDetail,
  tags: ['ai-generated'],
  args: { skill: skills.find((skill) => skill.name === 'bug-blame')! },
  decorators: [(Story) => <div className="flex h-[520px] w-[340px] max-w-full border border-line"><Story /></div>],
} satisfies Meta<typeof SkillDetail>
export default meta
type Story = StoryObj<typeof meta>

export const Installed: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    // The path is the thing a reader needs in order to go read the skill.
    await expect(canvas.getByText('*/skills/bug-blame/SKILL.md')).toBeInTheDocument()
    // Every granted command is shown, so the tool grant is auditable at a glance.
    await expect(canvas.getByText('rgt blame')).toBeInTheDocument()
    await expect(canvas.getByText('rgt show')).toBeInTheDocument()
  },
}

export const NoArguments: Story = { args: { skill: skills.find((skill) => skill.name === 'log')! } }

export const Proposed: Story = {
  args: { skill: skills.find((skill) => skill.name === 'token-audit')! },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await expect(canvas.getByText('proposed')).toBeInTheDocument()
    await expect(canvas.getByText(/not yet written/i)).toBeInTheDocument()
  },
}
