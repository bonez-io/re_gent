import type { Meta, StoryObj } from '@storybook/react-vite'
import { FileTree } from './FileTree'

const files = [
  { path: 'internal/server/read_api.go', blob_hash: 'a1', size: 18420 },
  { path: 'internal/server/read_api_test.go', blob_hash: 'b2', size: 9210 },
  { path: 'web/src/App.tsx', blob_hash: 'c3', size: 12540 },
  { path: 'web/src/components/FileTree.tsx', blob_hash: 'd4', size: 3410 },
  { path: 'README.md', blob_hash: 'e5', size: 8040 },
]

const meta = { component: FileTree, tags: ['ai-generated'], args: { files, selectedPath: 'web/src/App.tsx', onSelect: () => {} } } satisfies Meta<typeof FileTree>
export default meta
type Story = StoryObj<typeof meta>

export const Default: Story = {}
