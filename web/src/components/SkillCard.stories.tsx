import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, within } from 'storybook/test'
import { skills } from '../api/skills'
import { SkillCard } from './SkillCard'

const bugBlame = skills.find((skill) => skill.name === 'bug-blame')!
const fileCoupling = skills.find((skill) => skill.name === 'file-coupling')!
const proposed = skills.find((skill) => !skill.installed)!

const meta = {
  component: SkillCard,
  tags: ['ai-generated'],
  args: bugBlame,
  decorators: [(Story) => <div className="w-[240px] max-w-full"><Story /></div>],
} satisfies Meta<typeof SkillCard>
export default meta
type Story = StoryObj<typeof meta>

export const Available: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await expect(canvas.getByText('available')).toBeInTheDocument()
    await expect(canvas.getByText(bugBlame.title)).toBeInTheDocument()
  },
}

/** The card is title, one line, one badge — metadata belongs in the detail panel. */
export const CarriesNoMetadataClutter: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await expect(canvas.queryByText('Provenance')).not.toBeInTheDocument()
    await expect(canvas.queryByText(/blame · steps/)).not.toBeInTheDocument()
    await expect(canvas.queryByText(bugBlame.name)).not.toBeInTheDocument()
  },
}

/** A skill published to this server, rather than shipped with it. */
export const PublishedToThisServer: Story = {
  args: { ...bugBlame, origin: 'local' },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await expect(canvas.getByText('published')).toBeInTheDocument()
  },
}

/** Withheld from the default set; the card says so rather than implying it installs cleanly. */
export const Withheld: Story = {
  args: { ...bugBlame, withheld: 'describes conversation rewind, which rgt rewind does not do' },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await expect(canvas.getByText('withheld')).toBeInTheDocument()
  },
}

export const Selected: Story = { args: { selected: true } }

export const AnotherSkill: Story = { args: fileCoupling }

/** Proposed skills have no file on disk yet; the card must not imply otherwise. */
export const Proposed: Story = {
  args: proposed,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await expect(canvas.getByText('proposed')).toBeInTheDocument()
    await expect(canvas.queryByText('available')).not.toBeInTheDocument()
  },
}

export const LongDescription: Story = {
  args: {
    ...bugBlame,
    title: 'Regression hunter with an unusually long name',
    description: 'Trace a bug or incident back through captured history to the change that caused it, the prompt behind that change, the reasoning the agent recorded at the time, and every file that moved in the same turn.',
  },
}
