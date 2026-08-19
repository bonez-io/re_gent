import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect } from 'storybook/test'
import { ProjectSidebar } from './ProjectSidebar'

const meta = { component: ProjectSidebar, tags: ['ai-generated'], parameters: { layout: 'fullscreen' } } satisfies Meta<typeof ProjectSidebar>
export default meta
type Story = StoryObj<typeof meta>

export const Default: Story = { args: {} }
export const LongProjectName: Story = { args: { project: 'customer-support-agent-monorepo' } }
export const Managed: Story = { args: { deployment: 're_gent hosted' } }
export const FilesActive: Story = { args: { active: 'files' } }
export const CssCheck: Story = {
  args: {},
  play: async ({ canvas }) => {
    const sidebar = canvas.getByRole('complementary', { name: 'Project navigation' })
    await expect(getComputedStyle(sidebar).backgroundColor).toBe('rgb(15, 15, 15)')
  },
}
