import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, fn, userEvent } from 'storybook/test'
import { mockSessions } from '../mocks/regent'
import { SessionSearch } from './SessionSearch'

const selectSession = fn()
const meta = { component: SessionSearch, tags: ['ai-generated'], parameters: { layout: 'fullscreen' }, args: { sessions: mockSessions, selectedId: mockSessions[0].session_id, onSelect: selectSession }, decorators: [(Story) => <div className="h-[620px] w-[360px] bg-canvas"><Story /></div>] } satisfies Meta<typeof SessionSearch>
export default meta
type Story = StoryObj<typeof meta>

export const Default: Story = {
  play: async ({ canvas }) => {
    await expect(canvas.getByText(`6 of 6 captured`)).toBeVisible()
    await userEvent.type(canvas.getByRole('textbox', { name: 'Search sessions' }), 'relationship memory')
    await expect(canvas.getByText('Add relationship memory retrieval')).toBeVisible()
    await expect(canvas.queryByText('Stabilize reminder scheduling')).not.toBeInTheDocument()
  },
}

export const StructuredFilters: Story = {
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole('button', { name: 'Filters' }))
    await userEvent.selectOptions(canvas.getByLabelText('User'), 'Arad')
    await userEvent.selectOptions(canvas.getByLabelText('Coding agent'), 'Claude Code')
    await expect(canvas.getByText('2 of 6 captured')).toBeVisible()
    await expect(canvas.getByText('Add relationship memory retrieval')).toBeVisible()
    await expect(canvas.getByText('Review prompt injection boundaries')).toBeVisible()
  },
}

export const SemanticFallback: Story = {
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole('button', { name: '✦ Semantic' }))
    await expect(canvas.getByText('metadata fallback')).toBeVisible()
    await expect(canvas.getByRole('textbox', { name: 'Search sessions' })).toHaveAttribute('placeholder', 'Describe the work you remember…')
  },
}
