import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, userEvent, within } from 'storybook/test'
import { installCommand, installPrompt, skills } from '../api/skills'
import { SkillsScreen } from './SkillsScreen'

const meta = {
  component: SkillsScreen,
  tags: ['ai-generated'],
  parameters: { layout: 'fullscreen' },
  decorators: [(Story) => <div className="flex h-[640px] bg-page text-ink"><Story /></div>],
} satisfies Meta<typeof SkillsScreen>
export default meta
type Story = StoryObj<typeof meta>

const metaSkills = skills.filter((skill) => skill.category === 'meta')

export const Catalog: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await expect(canvas.getByRole('heading', { name: 'Skills', level: 1 })).toBeInTheDocument()
    for (const skill of skills) await expect(canvas.getAllByText(skill.title).length).toBeGreaterThan(0)
  },
}

export const FilterByCategory: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.click(canvas.getByRole('button', { name: 'Meta', pressed: false }))
    await expect(canvas.getByRole('button', { name: 'Meta' })).toHaveAttribute('aria-pressed', 'true')
    for (const skill of metaSkills) await expect(canvas.getAllByText(skill.title).length).toBeGreaterThan(0)
    await expect(canvas.queryByText('File coupling')).not.toBeInTheDocument()
  },
}

export const SearchNarrows: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.type(canvas.getByPlaceholderText('Filter skills…'), 'coupling')
    await expect(canvas.getAllByText('File coupling').length).toBeGreaterThan(0)
    await expect(canvas.queryByText('Rewind')).not.toBeInTheDocument()
  },
}

export const SelectingACardShowsItsDetail: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.click(canvas.getAllByText('File coupling')[0])
    await expect(canvas.getByText('.claude/skills/file-coupling/SKILL.md')).toBeInTheDocument()
  },
}

/** Ticking a checkbox must not move the detail panel: two intentions, two controls. */
export const CheckingDoesNotChangeDetail: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const before = canvas.getByText(`.claude/skills/${skills[0].name}/SKILL.md`)
    await expect(before).toBeInTheDocument()
    await userEvent.click(canvas.getByLabelText('Select File coupling for install'))
    await expect(canvas.getByText(`.claude/skills/${skills[0].name}/SKILL.md`)).toBeInTheDocument()
  },
}

/** The selection bar appears only once something is ticked, and names what was chosen. */
export const MultiSelectShowsInstallBar: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await expect(canvas.queryByRole('button', { name: /Copy command/ })).not.toBeInTheDocument()

    await userEvent.click(canvas.getByLabelText('Select Bug blame for install'))
    await userEvent.click(canvas.getByLabelText('Select File coupling for install'))

    await expect(canvas.getByText('2 selected')).toBeInTheDocument()
    await expect(canvas.getByRole('button', { name: 'Copy command' })).toBeInTheDocument()
    // The one-liner is visible without opening anything.
    await expect(canvas.getByText('rgt skill install bug-blame file-coupling')).toBeInTheDocument()
  },
}

/** The generated prompt names every chosen skill and surfaces the tool grant. */
export const GeneratedPromptIsVisible: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.click(canvas.getByLabelText('Select Bug blame for install'))
    await userEvent.click(canvas.getByLabelText('Select Context primer for install'))
    await userEvent.click(canvas.getByRole('button', { name: 'Show prompt' }))

    const text = canvas.getByText(/Run this to install 2 re_gent skills/)
    await expect(text).toBeInTheDocument()
    await expect(text.textContent).toContain('rgt skill install bug-blame context-primer')
    // The fallback for a machine without rgt is still spelled out.
    await expect(text.textContent).toContain('/api/skills/<name>')
  },
}

export const ClearingEmptiesTheSelection: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.click(canvas.getByLabelText('Select Bug blame for install'))
    await expect(canvas.getByText('1 selected')).toBeInTheDocument()
    await userEvent.click(canvas.getByRole('button', { name: 'Clear' }))
    await expect(canvas.queryByText('1 selected')).not.toBeInTheDocument()
  },
}

export const SelectAllShown: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.click(canvas.getByRole('button', { name: 'Meta', pressed: false }))
    await userEvent.click(canvas.getByRole('button', { name: 'Select all shown' }))
    await expect(canvas.getByText(`${metaSkills.length} selected`)).toBeInTheDocument()
  },
}

export const NoMatches: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.type(canvas.getByPlaceholderText('Filter skills…'), 'zzzznotaskill')
    await expect(canvas.getByText('No skills match that filter.')).toBeInTheDocument()
  },
}

/** The pure generator, pinned independently of the UI. */
export const PromptShapeIsStable: Story = {
  play: async () => {
    const one = installPrompt([skills[0]], 'http://example.test')
    await expect(one).toContain('Run this to install 1 re_gent skill')
    await expect(one).toContain(`http://example.test/api/skills/<name>`)
    await expect(installPrompt([], 'http://example.test')).toBe('')

    // The command is the thing most users paste, so pin its exact shape.
    await expect(installCommand([skills[0], skills[1]])).toBe(`rgt skill install ${skills[0].name} ${skills[1].name}`)
    await expect(installCommand([])).toBe('')
  },
}
