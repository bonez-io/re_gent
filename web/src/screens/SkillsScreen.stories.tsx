import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { expect, userEvent, within } from 'storybook/test'
import { installCommand, skills } from '../api/skills'
import { SkillsScreen } from './SkillsScreen'

const meta = {
  component: SkillsScreen,
  tags: ['ai-generated'],
  parameters: { layout: 'fullscreen' },
  decorators: [(Story) => {
    // No registry is reachable in Storybook, so fetchSkills falls back to the
    // bundled catalog — which is exactly the offline path worth pinning here.
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    return <QueryClientProvider client={client}><div className="flex h-[640px] bg-page text-ink"><Story /></div></QueryClientProvider>
  }],
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
    await expect(canvas.getByText(`.claude/skills/${skills[0].name}/SKILL.md`)).toBeInTheDocument()
    await userEvent.click(canvas.getByLabelText('Select File coupling for install'))
    await expect(canvas.getByText(`.claude/skills/${skills[0].name}/SKILL.md`)).toBeInTheDocument()
  },
}

/** The floating bar appears only once something is ticked. */
export const FloatingBarAppearsOnSelection: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await expect(canvas.queryByRole('button', { name: 'Copy command' })).not.toBeInTheDocument()

    await userEvent.click(canvas.getByLabelText('Select Bug blame for install'))
    await userEvent.click(canvas.getByLabelText('Select File coupling for install'))

    await expect(canvas.getByText('2 selected')).toBeInTheDocument()
    await expect(canvas.getByRole('button', { name: 'Copy command' })).toBeInTheDocument()
  },
}

/** The bar carries one action. No prompt preview, no second copy button. */
export const BarOffersOnlyTheCommand: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.click(canvas.getByLabelText('Select Bug blame for install'))

    await expect(canvas.getByRole('button', { name: 'Copy command' })).toBeInTheDocument()
    await expect(canvas.queryByRole('button', { name: /Copy prompt/ })).not.toBeInTheDocument()
    await expect(canvas.queryByRole('button', { name: /Show prompt/ })).not.toBeInTheDocument()
    await expect(canvas.queryByText(/Fetch its definition/)).not.toBeInTheDocument()
  },
}

export const ClearingEmptiesTheSelection: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.click(canvas.getByLabelText('Select Bug blame for install'))
    await expect(canvas.getByText('1 selected')).toBeInTheDocument()
    await userEvent.click(canvas.getByRole('button', { name: 'Clear selection' }))
    await expect(canvas.queryByText('1 selected')).not.toBeInTheDocument()
  },
}

export const NoMatches: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.type(canvas.getByPlaceholderText('Filter skills…'), 'zzzznotaskill')
    await expect(canvas.getByText('No skills match that filter.')).toBeInTheDocument()
  },
}

/** The command is what the button copies, so pin its exact shape. */
export const CommandShapeIsStable: Story = {
  play: async () => {
    await expect(installCommand([skills[0], skills[1]])).toBe(`rgt skill install ${skills[0].name} ${skills[1].name}`)
    await expect(installCommand([skills[0]])).toBe(`rgt skill install ${skills[0].name}`)
    await expect(installCommand([])).toBe('')

    // A registry-backed catalog names the registry, so the command fetches the
    // bytes the page showed rather than whatever the user's project is bound to.
    await expect(installCommand([skills[0]], 'http://srv.test')).toBe(`rgt skill install ${skills[0].name} --server http://srv.test`)
    await expect(installCommand([], 'http://srv.test')).toBe('')
  },
}
