import type { Meta, StoryObj } from '@storybook/react-vite'
import { blameLines } from '../mocks/regent'
import { FilesBlame } from './FilesBlame'

const meta = { component: FilesBlame, tags: ['ai-generated'], args: { lines: blameLines }, parameters: { layout: 'fullscreen' } } satisfies Meta<typeof FilesBlame>
export default meta
type Story = StoryObj<typeof meta>

export const SelectedProvenance: Story = {}
export const EarlierStep: Story = { args: { selectedHash: '41ac200' } }
export const UnattributedLine: Story = { args: { lines: [...blameLines, { number: 62, hash: 'unknown', author: '—', code: '// not present in the selected tree' }] } }
