import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { expect, userEvent, waitFor, within } from 'storybook/test'
import type { BlameResponse, LogStep } from '../api/types'
import { BlameView } from './BlameView'

const REPO = 'demo-repo'
const STEP_A = 'aaaaaaa1111111111111111111111111111111111111111111111111111111'
const STEP_B = 'bbbbbbb2222222222222222222222222222222222222222222222222222222'
const STEP_C = 'ccccccc3333333333333333333333333333333333333333333333333333333'

const line = (number: number, content: string, step?: string, origin?: string) => ({ number, content, step_hash: step, origin })

/** Three runs by two different agents, interleaved — the shape a real blame gutter has
 *  once a repo accumulates history, which no repo on the dev server does yet. */
const multiStep: BlameResponse = {
  step_hash: STEP_C,
  path: 'src/reminders/parser.ts',
  blob_hash: 'blob-1',
  lines: [
    line(1, "import { normalizeTimezone } from './timezone'", STEP_A, 'claude_code'),
    line(2, '', STEP_A, 'claude_code'),
    line(3, 'export function parseNaturalDate(input: string) {', STEP_A, 'claude_code'),
    line(4, '  const parsed = parse(input)', STEP_B, 'codex'),
    line(5, '  // conversion belongs in the service, once the profile timezone is known', STEP_B, 'codex'),
    line(6, '  return { parsed, sourceTimezone: null }', STEP_B, 'codex'),
    line(7, '}', STEP_A, 'claude_code'),
    line(8, '', STEP_C, 'opencode'),
    line(9, 'export const DEFAULT_TZ = "UTC"', STEP_C, 'opencode'),
  ],
}

const stepFixture: LogStep = {
  hash: STEP_B,
  timestamp: '2026-08-18T12:24:28Z',
  origin: 'codex',
  parent: STEP_A,
  tool: 'Edit',
  tool_use_id: 'toolu_01ABC',
  causes: [{ tool: 'Edit', tool_use_id: 'toolu_01ABC', args: { path: 'src/reminders/parser.ts' }, result: 'ok' }],
  files: ['src/reminders/parser.ts'],
  args: {},
  result: '',
  messages: [],
}

/** Surfaces the router location so a story can assert that clicking a step navigated. */
function LocationProbe() {
  const location = useLocation()
  return <span data-testid="location">{location.pathname}{location.search}</span>
}

const withClient = (Story: () => React.ReactElement) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  // BlameView navigates into the session transcript, so it needs a router context.
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={['/repos/demo-repo/files']}><div className="flex h-[520px] w-full flex-col bg-inset text-ink"><Story /><LocationProbe /></div></MemoryRouter></QueryClientProvider>
}

const meta = {
  component: BlameView,
  tags: ['ai-generated'],
  parameters: { layout: 'fullscreen' },
  args: { repoId: REPO, data: multiStep },
  beforeEach({ msw }) {
    msw.use(http.get('/:repo/api/steps/:hash', () => HttpResponse.json(stepFixture)))
  },
  decorators: [withClient],
} satisfies Meta<typeof BlameView>
export default meta
type Story = StoryObj<typeof meta>

/** Attribution is stated once per run, not repeated on every line. */
export const MultipleSteps: Story = {
  play: async ({ canvas }) => {
    // Four runs across nine lines: A, B, A again, C — so A is stated twice.
    await expect(canvas.getAllByText('aaaaaaa')).toHaveLength(2)
    await expect(canvas.getByText('bbbbbbb')).toBeVisible()
    await expect(canvas.getByText('ccccccc')).toBeVisible()
  },
}

/** A step that records no session cannot be linked into a transcript, so the detail panel
 *  is shown instead — the click is never a dead end. */
export const StepWithoutSession: Story = {
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByText('bbbbbbb'))
    const panel = await within(document.body).findByRole('dialog')
    await expect(panel).toBeVisible()
  },
}

/** The normal path: a blamed line links through to the session that produced it. */
export const LinksToSession: Story = {
  beforeEach({ msw }) {
    msw.use(http.get('/:repo/api/steps/:hash', () => HttpResponse.json({ ...stepFixture, session_id: 'claude_code--sess-1' })))
  },
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByText('bbbbbbb'))
    // Navigation waits on the step resolving, so poll rather than assert once.
    await waitFor(() => expect(canvas.getByTestId('location').textContent).toContain(`/sessions/claude_code--sess-1?step=${STEP_B}`))
  },
}

/** Lines re_gent has no provenance for must read as unknown, not as someone's work. */
export const UnattributedLines: Story = {
  args: {
    data: { ...multiStep, lines: [line(1, 'const legacy = true'), line(2, 'const known = 1', STEP_A, 'claude_code')] },
  },
  play: async ({ canvas }) => {
    await expect(canvas.getByText('—')).toBeVisible()
    await expect(canvas.getByText('aaaaaaa')).toBeVisible()
  },
}

export const BinaryFile: Story = {
  args: {
    data: { ...multiStep, path: 'assets/logo.png', lines: [line(1, `PNG${String.fromCharCode(0, 13, 10, 26)}binary`, STEP_A, 'claude_code')] },
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByText(/Binary file/)).toBeVisible()
  },
}

export const EmptyFile: Story = {
  args: { data: { ...multiStep, path: 'src/empty.ts', lines: [] } },
  play: async ({ canvas }) => {
    await expect(await canvas.findByText(/empty at this step/)).toBeVisible()
  },
}

export const TooLargeFile: Story = {
  args: {
    data: {
      ...multiStep,
      path: 'src/huge.ts',
      lines: Array.from({ length: 5200 }, (_, index) => line(index + 1, `const line${index} = ${index}`, STEP_A, 'claude_code')),
    },
  },
  play: async ({ canvas }) => {
    await expect(canvas.getByText(/showing first 500 lines/)).toBeVisible()
    await expect(canvas.queryByText('const line5199 = 5199')).not.toBeInTheDocument()
  },
}
