import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect } from 'storybook/test'
import { FileDiffView, type DiffLine, type FileDiff } from './FileDiffView'

const ctx = (old_number: number, new_number: number, content: string): DiffLine => ({ kind: 'context', old_number, new_number, content })
const add = (new_number: number, content: string): DiffLine => ({ kind: 'add', new_number, content })
const del = (old_number: number, content: string): DiffLine => ({ kind: 'delete', old_number, content })

const modifiedDiff: FileDiff = {
  path: 'src/reminders/parser.ts',
  status: 'modified',
  is_binary: false,
  truncated: false,
  additions: 4,
  deletions: 2,
  hunks: [
    {
      old_start: 9, old_lines: 6, new_start: 9, new_lines: 7,
      lines: [
        ctx(9, 9, 'function parseReminder(input: string, timezone: string) {'),
        del(10, '  const parsed = naiveParse(input)'),
        add(10, '  const parsed = naiveParse(input, { preserveSource: true })'),
        add(11, '  const zone = normalizeTimezone(timezone)'),
        ctx(11, 12, '  if (!parsed) throw new Error(\'unparseable input\')'),
        ctx(12, 13, '  return { ...parsed, timezone: zone }'),
        ctx(13, 14, '}'),
      ],
    },
    {
      old_start: 40, old_lines: 4, new_start: 41, new_lines: 5,
      lines: [
        ctx(40, 41, 'export function scheduleReminder(input: string, timezone: string) {'),
        del(41, '  const reminder = parseReminder(input)'),
        add(42, '  const reminder = parseReminder(input, timezone)'),
        add(43, '  emitScheduled(reminder)'),
        ctx(42, 44, '  return reminder.dueAt'),
      ],
    },
  ],
}

const addedDiff: FileDiff = {
  path: 'src/reminders/parser.test.ts',
  status: 'added',
  is_binary: false,
  truncated: false,
  additions: 5,
  deletions: 0,
  hunks: [{
    old_start: 0, old_lines: 0, new_start: 1, new_lines: 5,
    lines: [
      add(1, 'import { parseReminder } from \'./parser\''),
      add(2, ''),
      add(3, 'test(\'parses a plain reminder\', () => {'),
      add(4, '  expect(parseReminder(\'tomorrow 9am\', \'UTC\')).toBeDefined()'),
      add(5, '})'),
    ],
  }],
}

const deletedDiff: FileDiff = {
  path: 'src/reminders/legacy-parser.ts',
  status: 'deleted',
  is_binary: false,
  truncated: false,
  additions: 0,
  deletions: 3,
  hunks: [{
    old_start: 1, old_lines: 3, new_start: 0, new_lines: 0,
    lines: [
      del(1, 'export function legacyParse(input: string) {'),
      del(2, '  return input.trim()'),
      del(3, '}'),
    ],
  }],
}

const binaryDiff: FileDiff = {
  path: 'assets/logo.png',
  status: 'modified',
  is_binary: true,
  truncated: false,
  additions: 0,
  deletions: 0,
  hunks: [],
}

const truncatedDiff: FileDiff = {
  path: 'internal/store/objects.go',
  status: 'modified',
  is_binary: false,
  truncated: true,
  additions: 812,
  deletions: 340,
  hunks: [{
    old_start: 1, old_lines: 4, new_start: 1, new_lines: 4,
    lines: [
      ctx(1, 1, 'package store'),
      ctx(2, 2, ''),
      del(3, 'func Open(path string) (*Store, error) {'),
      add(3, 'func Open(path string, opts ...Option) (*Store, error) {'),
    ],
  }],
}

const emptyHunksDiff: FileDiff = {
  path: 'scripts/deploy.sh',
  status: 'modified',
  is_binary: false,
  truncated: false,
  additions: 0,
  deletions: 0,
  hunks: [],
}

const longDiff: FileDiff = {
  path: 'internal/index/schema.sql',
  status: 'modified',
  is_binary: false,
  truncated: false,
  additions: 120,
  deletions: 0,
  hunks: [{
    old_start: 1, old_lines: 1, new_start: 1, new_lines: 121,
    lines: [
      ctx(1, 1, '-- schema'),
      ...Array.from({ length: 120 }, (_, index) => add(index + 2, `CREATE INDEX idx_step_${index} ON steps(hash_${index});`)),
    ],
  }],
}

const meta = { component: FileDiffView, tags: ['ai-generated'], args: { diff: modifiedDiff, href: '/repos/demo-repo/files?path=src%2Freminders%2Fparser.ts' } } satisfies Meta<typeof FileDiffView>
export default meta
type Story = StoryObj<typeof meta>

export const Default: Story = {}

export const AddedFile: Story = { args: { diff: addedDiff, href: '/repos/demo-repo/files?path=src%2Freminders%2Fparser.test.ts' } }

export const DeletedFile: Story = {
  // href is deliberately supplied here even though the file no longer exists at this step —
  // the component must refuse to link it regardless.
  args: { diff: deletedDiff, href: '/repos/demo-repo/files?path=src%2Freminders%2Flegacy-parser.ts' },
  play: async ({ canvasElement }) => {
    await expect(canvasElement.querySelector('a')).toBeNull()
  },
}

export const Binary: Story = {
  args: { diff: binaryDiff, href: '/repos/demo-repo/files?path=assets%2Flogo.png' },
  play: async ({ canvas }) => {
    await expect(canvas.getByText('Binary file — preview not available.')).toBeInTheDocument()
    // No stray byte soup — binary content is never dumped into the DOM.
    await expect(canvas.queryByText(/�/)).not.toBeInTheDocument()
  },
}

export const Truncated: Story = {
  args: { diff: truncatedDiff, href: '/repos/demo-repo/files?path=internal%2Fstore%2Fobjects.go' },
  play: async ({ canvas, canvasElement }) => {
    await expect(canvas.getByText('Diff truncated — showing partial changes only.')).toBeInTheDocument()
    // The partial hunk that *is* available still renders alongside the warning. Checked via
    // textContent, not getByText, because syntax highlighting may have already split the line
    // into one span per token by the time this assertion runs.
    await expect(canvasElement.textContent ?? '').toContain('package store')
  },
}

export const EmptyHunks: Story = { args: { diff: emptyHunksDiff, href: undefined } }

export const NoHref: Story = {
  args: { diff: modifiedDiff, href: undefined },
  play: async ({ canvasElement, canvas }) => {
    await expect(canvasElement.querySelector('a')).toBeNull()
    await expect(canvas.getByText('src/reminders/parser.ts')).toBeInTheDocument()
  },
}

export const VeryLongDiff: Story = { args: { diff: longDiff } }

// Syntax highlighting splits a source line into one span per token, so the full line text is
// never a single text node — check via textContent rather than getByText's exact-string match.
const hasParseReminderLine = (canvasElement: HTMLElement) => (canvasElement.textContent ?? '').includes('function parseReminder(input: string, timezone: string) {')

export const CollapseToggle: Story = {
  args: { diff: modifiedDiff, defaultOpen: true },
  play: async ({ canvas, canvasElement, userEvent }) => {
    const toggle = canvas.getByRole('button', { name: 'Collapse src/reminders/parser.ts diff' })
    await expect(toggle).toHaveAttribute('aria-expanded', 'true')
    await expect(hasParseReminderLine(canvasElement)).toBe(true)

    await userEvent.click(toggle)
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await expect(toggle).toHaveAccessibleName('Expand src/reminders/parser.ts diff')
    await expect(hasParseReminderLine(canvasElement)).toBe(false)

    await userEvent.click(toggle)
    await expect(toggle).toHaveAttribute('aria-expanded', 'true')
    await expect(hasParseReminderLine(canvasElement)).toBe(true)
  },
}

export const CollapsedByDefault: Story = {
  args: { diff: modifiedDiff, defaultOpen: false },
  play: async ({ canvas, canvasElement }) => {
    const toggle = canvas.getByRole('button', { name: 'Expand src/reminders/parser.ts diff' })
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await expect(hasParseReminderLine(canvasElement)).toBe(false)
  },
}
