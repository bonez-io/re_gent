import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { expect, waitFor, within } from 'storybook/test'
import type { StepDiffResponse } from '../api/types'
import type { FileDiff } from './FileDiffView'
import { ToolCallGroup } from './ToolCallGroup'

const calls = [
  { id: 'read', tool: 'Read', summary: 'src/reminders/parser.ts', detail: ['Read 184 lines', 'Located parseReminder at line 42'] },
  { id: 'edit', tool: 'Edit', summary: 'src/reminders/parser.ts', detail: ['+ preserve timezone metadata'] },
  { id: 'test', tool: 'Bash', summary: 'pnpm test parser', detail: ['✓ 18 tests passed'] },
]
// Fake counts on purpose: the adapter that builds `files` hardcodes additions/deletions to 0,
// so a story that only rendered chips would silently pin the "+0" lie. These stories click
// through to a fetched diff, where the real numbers below (18/6) come from `parserDiff` instead.
const files = [{ path: 'parser.ts', additions: 0, deletions: 0 }, { path: 'parser.test.ts', additions: 0, deletions: 0 }]

const REPO = 'demo-repo'
const STEP = 'stepstep1111111111111111111111111111111111111111111111111111111'

const parserDiff: FileDiff = {
  path: 'parser.ts',
  status: 'modified',
  is_binary: false,
  truncated: false,
  additions: 18,
  deletions: 6,
  hunks: [{
    old_start: 40,
    old_lines: 4,
    new_start: 40,
    new_lines: 5,
    lines: [
      { kind: 'context', old_number: 40, new_number: 40, content: 'export function parseReminder(input: string) {' },
      { kind: 'delete', old_number: 41, content: '  return legacyParse(input)' },
      { kind: 'add', new_number: 41, content: '  const parsed = legacyParse(input)' },
      { kind: 'add', new_number: 42, content: '  return { ...parsed, timezone: input.timezone }' },
      { kind: 'context', old_number: 42, new_number: 43, content: '}' },
    ],
  }],
}

// Only `parser.ts` is present — `parser.test.ts` was in the tool call's file list (it was
// read during the turn) but the step diff has no entry for it, the real shape of "read but
// not modified".
const diffResponse: StepDiffResponse = { step_hash: STEP, parent_hash: 'parent-hash', total_files: 1, files: [parserDiff] }

const withClient = (Story: () => React.ReactElement) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={client}><Story /></QueryClientProvider>
}

const meta = {
  component: ToolCallGroup,
  tags: ['ai-generated'],
  args: { calls, files },
  decorators: [withClient],
} satisfies Meta<typeof ToolCallGroup>
export default meta
type Story = StoryObj<typeof meta>

export const Default: Story = {
  play: async ({ canvas, userEvent }) => {
    const group = canvas.getByRole('button', { name: 'Used tools' })
    await expect(group).toHaveAttribute('aria-expanded', 'false')
    await userEvent.click(group)
    await expect(group).toHaveAttribute('aria-expanded', 'true')
    await waitFor(() => expect(canvas.getByText('+ preserve timezone metadata')).toBeVisible())
  },
}
export const Collapsed: Story = { args: { defaultOpen: false } }
export const Failed: Story = { args: { calls: [...calls, { id: 'failed', tool: 'Bash', summary: 'pnpm test', detail: ['✗ expected 2 reminders'], status: 'failed' }] } }

export const LargePayload: Story = {
  args: { calls: [{ id: 'image', tool: 'Read', summary: 'captured screenshot', detail: [`data:image/jpeg;base64,${'A'.repeat(100_000)}`] }] },
  play: async ({ canvas, canvasElement, userEvent }) => {
    const group = canvas.getByRole('button', { name: 'Read files' })
    // Closed tool groups do not mount their payload at all.
    await expect(canvasElement.textContent).not.toContain('data:image/jpeg')
    await userEvent.click(group)
    await waitFor(() => expect(canvas.getByText(/Showing first 4,000 of 100,023 characters/)).toBeVisible())
    await expect((canvasElement.textContent ?? '').length).toBeLessThan(5000)
  },
}

/** The chip is a dead button without both `repoId` and `stepHash` — there is no step to
 *  fetch a diff for, so it says so instead of spinning forever. */
export const NoStepContext: Story = {
  args: { defaultOpen: true },
  play: async ({ canvas, userEvent }) => {
    const chip = canvas.getByRole('button', { name: /^parser\.ts/, expanded: false })
    await userEvent.click(chip)
    await expect(await canvas.findByText('Diff unavailable for this tool call.')).toBeVisible()
  },
}

/** The normal path: clicking a chip fetches the step diff once, renders the real hunk, and
 *  fills in the chip's counts from the response — never from the adapter's hardcoded 0/0. */
export const ExpandsRealDiff: Story = {
  args: { defaultOpen: true, repoId: REPO, stepHash: STEP },
  beforeEach({ msw }) {
    msw.use(http.get('/:repo/api/diff', () => HttpResponse.json(diffResponse)))
  },
  play: async ({ canvas, userEvent }) => {
    // Before the chip is opened, nothing has been fetched — no count is shown at all,
    // not the fabricated "+0" the adapter would otherwise supply.
    const chip = canvas.getByRole('button', { name: /^parser\.ts/, expanded: false })
    await expect(within(chip).queryByText('+0')).not.toBeInTheDocument()

    await userEvent.click(chip)
    // getByText's default normalizer trims leading whitespace, so the assertion drops the
    // diff line's two-space indent rather than fail to match it.
    await waitFor(() => expect(canvas.getByText('const parsed = legacyParse(input)')).toBeVisible())
    await expect(canvas.getByRole('button', { name: /Collapse parser\.ts diff/ })).toBeVisible()

    // The chip itself now reflects the real 18/6 from the fetched diff.
    const openChip = canvas.getByRole('button', { name: /^parser\.ts/, expanded: true })
    await expect(within(openChip).getByText('+18')).toBeVisible()
    await expect(within(openChip).getByText('−6')).toBeVisible()
  },
}

/** `parser.test.ts` was touched by the tool call but the step diff has no entry for it —
 *  read, not modified. That reads as a plain statement, not an empty box or a spinner. */
export const NoMatchingDiffEntry: Story = {
  args: { defaultOpen: true, repoId: REPO, stepHash: STEP },
  beforeEach({ msw }) {
    msw.use(http.get('/:repo/api/diff', () => HttpResponse.json(diffResponse)))
  },
  play: async ({ canvas, userEvent }) => {
    const chip = canvas.getByRole('button', { name: /parser\.test\.ts/, expanded: false })
    await userEvent.click(chip)
    await expect(await canvas.findByText('No changes recorded for this file.')).toBeVisible()
    await expect(within(chip).queryByText(/^\+/)).not.toBeInTheDocument()
  },
}

/** An unreachable server must not leave the expanded chip permanently blank. */
export const DiffFetchError: Story = {
  args: { defaultOpen: true, repoId: REPO, stepHash: STEP },
  beforeEach({ msw }) {
    msw.use(http.get('/:repo/api/diff', () => new HttpResponse(null, { status: 500 })))
  },
  play: async ({ canvas, userEvent }) => {
    const chip = canvas.getByRole('button', { name: /parser\.ts/, expanded: false })
    await userEvent.click(chip)
    await expect(await canvas.findByText(/Couldn't load the diff/)).toBeVisible()
  },
}
