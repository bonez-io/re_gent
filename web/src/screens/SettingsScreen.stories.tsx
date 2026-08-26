import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect } from 'storybook/test'
import { SettingsScreen } from './SettingsScreen'

const meta = { component: SettingsScreen, tags: ['ai-generated'], parameters: { layout: 'fullscreen' }, args: { section: 'general' } } satisfies Meta<typeof SettingsScreen>
export default meta
type Story = StoryObj<typeof meta>

export const General: Story = {}
export const Users: Story = { args: { section: 'users' } }
export const Data: Story = {
  args: { section: 'data' },
  play: async ({ canvas }) => {
    await expect(canvas.getByRole('heading', { name: 'Data' })).toBeVisible()
    await expect(canvas.getByText('Semantic index')).toBeVisible()
    await expect(canvas.getByText('Not connected')).toBeVisible()
  },
}
