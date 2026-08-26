import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import { expect, fn } from 'storybook/test'
import { StepDetailPanel } from './StepDetailPanel'
import type { LogStep } from '../api/types'

// Mirrors the panel's private truncateHash(front=10, back=6) so tests can compute the exact
// accessible names it produces without duplicating that constant across files.
const truncate = (hash: string, front = 10, back = 6) => (hash.length <= front + back + 1 ? hash : `${hash.slice(0, front)}…${hash.slice(-back)}`)

const populatedStep: LogStep = {
  hash: 'b4b7e2a9c3d15f608e19a2b7c4d3e1f0a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4',
  timestamp: '2026-08-19T14:02:31.000Z',
  origin: 'claude-code',
  parent: '9a1f2e3d4c5b6a7988776655443322110ffeeddccbbaa9988776655443322',
  tool: 'Edit',
  tool_use_id: 'toolu_01Xk3Nq8vHqzQpR2m5T7wYbZ',
  causes: [
    { tool: 'Read', tool_use_id: 'toolu_01Read001', args: { file_path: '/repo/internal/capture/hooks.go' }, result: '1\tpackage capture\n2\t\n3\tfunc Handle() error {\n4\t\treturn nil\n5\t}\n' },
    { tool: 'Edit', tool_use_id: 'toolu_01Edit002', args: { file_path: '/repo/internal/capture/hooks.go', old_string: 'return nil', new_string: 'return recordStep(ctx)' }, result: { applied: true, hunks: 1 } },
    { tool: 'Bash', tool_use_id: 'toolu_01Bash003', args: { command: 'go test ./internal/capture/...' }, result: 'ok  	re_gent/internal/capture	0.412s\n' },
  ],
  files: ['internal/capture/hooks.go', 'internal/capture/hooks_test.go'],
  args: { file_path: '/repo/internal/capture/hooks.go' },
  result: { applied: true },
  messages: [],
  session_id: 'sess_9f8e7d6c5b4a3928',
  tree: 'c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4',
  author: { name: 'Shay Livne', email: 'shay@bonez.io' },
  usage: { input_tokens: 18422, output_tokens: 1531, cache_read_tokens: 204800 },
  effects: [{ type: 'http', description: 'POST https://api.example.com/webhook (uncompensated)' }],
}

const legacyStep: LogStep = {
  hash: 'a1e2d3c4b5a6978869504132435465768798a9bac0d1e2f3405162738495a6',
  timestamp: '',
  origin: '',
  parent: '',
  tool: '',
  tool_use_id: '',
  causes: [],
  files: [],
  args: undefined,
  result: undefined,
  messages: [],
}

const largeResult = Array.from({ length: 4000 }, (_, index) => `line ${index + 1}: the quick brown fox jumps over the lazy dog`).join('\n')
const largeResultStep: LogStep = {
  ...populatedStep,
  hash: 'd1e2c3f4a5b6978869504132435465768798a9bac0d1e2f3405162738495c7',
  causes: [
    { tool: 'Bash', tool_use_id: 'toolu_01BigOutput', args: { command: 'cat internal/store/objects.go' }, result: largeResult },
  ],
}

const meta = {
  component: StepDetailPanel,
  tags: ['ai-generated'],
  args: { onClose: fn() },
  // Only rendered when repoId is supplied, but present on every story for consistency (see
  // StepMarker.stories.tsx for the same pattern).
  decorators: [(StoryFn) => <MemoryRouter><StoryFn /></MemoryRouter>],
} satisfies Meta<typeof StepDetailPanel>
export default meta
type Story = StoryObj<typeof meta>

export const Populated: Story = {
  args: { step: populatedStep },
  play: async ({ canvas }) => {
    const dialog = canvas.getByRole('dialog', { name: /Step/ })
    await expect(dialog).toBeInTheDocument()
    await expect(canvas.getByRole('button', { name: 'Close step details' })).toHaveFocus()
    await expect(canvas.getByText('Shay Livne · shay@bonez.io')).toBeInTheDocument()
    await expect(canvas.getByText('claude-code')).toBeInTheDocument()
  },
}

export const LegacySparse: Story = {
  args: { step: legacyStep },
  play: async ({ canvas, canvasElement }) => {
    await expect(canvas.getByRole('dialog')).toBeInTheDocument()
    // Every optional/blank field should degrade to a placeholder, never a raw undefined/NaN/Invalid Date.
    await expect(canvas.getAllByText('not recorded').length).toBeGreaterThan(0)
    await expect(canvas.getAllByText('—').length).toBeGreaterThan(0)
    const text = canvasElement.textContent ?? ''
    await expect(text).not.toMatch(/undefined/)
    await expect(text).not.toMatch(/\bNaN\b/)
    await expect(text).not.toMatch(/Invalid Date/)
    await expect(text).not.toMatch(/\bnull\b/)
    // Sections with no data at all (Usage, Causes, Files, Effects) are omitted, not rendered empty.
    await expect(canvas.queryByText('Causes')).not.toBeInTheDocument()
    await expect(canvas.queryByText('Usage')).not.toBeInTheDocument()
    await expect(canvas.queryByText('Files')).not.toBeInTheDocument()
    await expect(canvas.queryByText('Effects')).not.toBeInTheDocument()
  },
}

export const LargeToolResult: Story = {
  args: { step: largeResultStep },
  play: async ({ canvas, canvasElement, userEvent }) => {
    const cause = canvas.getByRole('button', { name: /Bash/ })
    await userEvent.click(cause)
    const expandButton = await canvas.findByText(/Show more/)
    await expect(expandButton).toBeInTheDocument()
    // Only the truncated preview is in the DOM until the reader opts into more.
    const text = canvasElement.textContent ?? ''
    await expect(text.length).toBeLessThan(largeResult.length)
  },
}

export const Pending: Story = {
  args: { isPending: true },
  play: async ({ canvas }) => {
    await expect(canvas.getByRole('dialog')).toBeInTheDocument()
    await expect(canvas.getByText('Loading step…')).toBeInTheDocument()
  },
}

export const ErrorState: Story = {
  args: { error: new Error('step not found on server') },
  play: async ({ canvas }) => {
    await expect(canvas.getByText('Failed to load step: step not found on server')).toBeInTheDocument()
  },
}

/** With repoId supplied, Files/Session/Tree/Parent all become links into the right destination —
 *  files and the tree both route through this step's own hash, parent routes through its hash. */
export const LinksWithRepoId: Story = {
  args: { step: populatedStep, repoId: 'demo-repo' },
  play: async ({ canvas }) => {
    const filesHref = (stepHash: string) => `/repos/demo-repo/files?step=${stepHash}`

    const parentLink = canvas.getByRole('link', { name: `Browse files at step ${truncate(populatedStep.parent)}` })
    await expect(parentLink).toHaveAttribute('href', filesHref(populatedStep.parent))

    const treeLink = canvas.getByRole('link', { name: `Browse files at step ${truncate(populatedStep.hash)}` })
    await expect(treeLink).toHaveAttribute('href', filesHref(populatedStep.hash))

    const sessionLink = canvas.getByRole('link', { name: `View session ${populatedStep.session_id}, highlighting step ${truncate(populatedStep.hash)}` })
    await expect(sessionLink).toHaveAttribute('href', `/repos/demo-repo/sessions/${populatedStep.session_id}?step=${populatedStep.hash}`)

    const fileLink = canvas.getByRole('link', { name: `View internal/capture/hooks.go at step ${truncate(populatedStep.hash)}` })
    await expect(fileLink).toHaveAttribute('href', `${filesHref(populatedStep.hash)}&path=internal%2Fcapture%2Fhooks.go`)
  },
}

/** Without repoId, every one of those same values renders as plain text — a step detail panel
 *  is presentational and must never guess a URL it wasn't given. */
export const NoLinksWithoutRepoId: Story = {
  args: { step: populatedStep },
  play: async ({ canvas, canvasElement }) => {
    await expect(canvasElement.querySelectorAll('a')).toHaveLength(0)
    await expect(canvas.getByText(populatedStep.session_id!)).toBeInTheDocument()
    await expect(canvas.getByText('internal/capture/hooks.go')).toBeInTheDocument()
  },
}

/** The root step has no parent — even with repoId supplied, that field must stay `—` and
 *  unlinked rather than pointing a step at itself or at a made-up destination. */
export const RootStepParentStaysUnlinked: Story = {
  args: { step: { ...populatedStep, parent: '' }, repoId: 'demo-repo' },
  play: async ({ canvas }) => {
    await expect(canvas.getByText('—')).toBeInTheDocument()
    await expect(canvas.queryByRole('link', { name: /Browse files at step 9a1f/ })).not.toBeInTheDocument()
    // The tree link (a distinct field) is still present — only parent degrades.
    await expect(canvas.getByRole('link', { name: `Browse files at step ${truncate(populatedStep.hash)}` })).toBeInTheDocument()
  },
}

export const EscapeCloses: Story = {
  args: { step: populatedStep, onClose: fn() },
  play: async ({ canvas, userEvent, args }) => {
    await expect(canvas.getByRole('dialog')).toBeInTheDocument()
    await userEvent.keyboard('{Escape}')
    await expect(args.onClose).toHaveBeenCalledTimes(1)
  },
}
