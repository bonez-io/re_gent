import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, userEvent, waitFor } from 'storybook/test'
import { ProjectSidebar } from './ProjectSidebar'

const meta = { component: ProjectSidebar, tags: ['ai-generated'], parameters: { layout: 'fullscreen' }, args: { project: 'github.com-bonez-io-re_gent', userName: 'Shay', userDetail: 'Local workspace' } } satisfies Meta<typeof ProjectSidebar>
export default meta
type Story = StoryObj<typeof meta>

export const Default: Story = {}
export const LongProjectName: Story = { args: { project: 'customer-support-agent-monorepo-with-a-long-name' } }
export const FilesActive: Story = { args: { active: 'files' } }

export const SettingsOpen: Story = {
  args: { active: 'settings', settingsSection: 'status' },
  play: async ({ canvas }) => {
    await expect(canvas.getByRole('button', { name: 'Settings' })).toHaveAttribute('aria-expanded', 'true')
    await expect(canvas.getByRole('button', { name: 'Status' })).toHaveAttribute('aria-current', 'page')
  },
}

/** Icon rail: labels collapse out of view but every control keeps an accessible name. */
export const Collapsed: Story = {
  args: { collapsed: true },
  play: async ({ canvas }) => {
    for (const label of ['Sessions', 'Team', 'Browse', 'Skills', 'Settings']) await expect(canvas.getByRole('button', { name: label })).toBeInTheDocument()
    await expect(canvas.getByRole('button', { name: 'User: Shay' })).toBeInTheDocument()
    await expect(canvas.getByRole('button', { name: 'Expand sidebar' })).toBeInTheDocument()
  },
}

export const ToggleInteraction: Story = {
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole('button', { name: 'Collapse sidebar' }))
    await expect(canvas.getByRole('button', { name: 'Expand sidebar' })).toBeInTheDocument()
    await userEvent.click(canvas.getByRole('button', { name: 'Expand sidebar' }))
    await expect(canvas.getByRole('button', { name: 'Collapse sidebar' })).toBeInTheDocument()
  },
}

export const KeyboardReveal: Story = {
  args: { collapsed: true },
  play: async ({ canvas }) => {
    canvas.getByRole('link', { name: 'Visit re_gent' }).focus()
    await userEvent.tab()
    const toggle = canvas.getByRole('button', { name: 'Expand sidebar' })
    await waitFor(() => expect(toggle).toHaveFocus())
    await waitFor(() => expect(getComputedStyle(toggle).opacity).toBe('1'))
  },
}
