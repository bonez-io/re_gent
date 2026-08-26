import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { expect, spyOn, waitFor, within } from 'storybook/test'
import type { TranscriptEntry } from '../api/types'
import { transcript } from '../mocks/regent'
import { ConversationTranscript, STEP_HIGHLIGHT_MS } from './ConversationTranscript'

const meta = {
  component: ConversationTranscript,
  tags: ['ai-generated'],
  // repoId is on by default so step markers render as real links in the common stories below —
  // NoLinksWithoutRepoId overrides it to cover the other side (plain text, no dead links).
  args: { entries: transcript, repoId: 'demo-repo' },
  parameters: { layout: 'fullscreen' },
  // ToolCallGroup fetches step diffs lazily, so the transcript needs a query client even
  // though these stories never expand a file chip.
  decorators: [(StoryFn) => <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter><StoryFn /></MemoryRouter></QueryClientProvider>],
} satisfies Meta<typeof ConversationTranscript>
export default meta
type Story = StoryObj<typeof meta>

export const TwoCapturedTurns: Story = {}
export const SingleTurn: Story = { args: { entries: transcript.slice(0, 9) } }
export const ToolFailure: Story = { args: { entries: [{ type: 'user', id: 'f1', at: '14:22:01', content: 'Run the reminder suite and fix the failing boundary case.' }, { type: 'tools', id: 'f2', at: '14:22:06', calls: [{ id: 'failed', tool: 'Bash', summary: 'pnpm test reminders', detail: ['✗ expected 09:00, received 10:00', '1 failed · 66 passed'], status: 'failed' }] }] } }
export const LegacyTranscript: Story = { args: { entries: [{ type: 'user', id: 'l1', at: 'unknown', content: 'Initial onboarding prompt' }, { type: 'assistant', id: 'l2', at: 'unknown', content: 'This conversation predates normalized reasoning and tool capture.' }] } }

/** Speaker + timestamp never unmount — only their opacity toggles — so a screen reader gets
    them on every message regardless of hover or focus state. */
export const MessageMetaStaysInAccessibilityTree: Story = {
  args: { entries: transcript.slice(0, 2) },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await expect(canvas.getByText('User')).toBeInTheDocument()
    await expect(canvas.getByText('User')).not.toBeVisible()
    await expect(canvas.getByText('13:04:12')).toBeInTheDocument()
    await expect(canvas.getByText('Agent')).toBeInTheDocument()
  },
}

/** The reveal is not mouse-only: focusing the reasoning toggle inside a message reveals that
    message's meta via focus-within, same as hover does. */
export const MessageMetaKeyboardReveal: Story = {
  args: { entries: transcript.slice(7, 8) },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const label = canvas.getByText('Reason')
    const toggle = canvas.getByRole('button', { name: 'Toggle reasoning' })
    await expect(getComputedStyle(label.parentElement!).opacity).toBe('0')
    toggle.focus()
    await expect(toggle).toHaveFocus()
    await waitFor(() => expect(getComputedStyle(label.parentElement!).opacity).toBe('1'))
  },
}

// A blame link's scrollIntoView call happens inside a mount-time effect, before a story's play()
// function gets to run — a decorator's render-phase body executes before any child effect fires,
// so arming the spy there (rather than in play) reliably catches it. Shared per-story so play can
// read the same instance the decorator just installed.
const scrollSpy: { instance?: ReturnType<typeof spyOn> } = {}
function armScrollIntoViewSpy() {
  scrollSpy.instance?.mockRestore()
  scrollSpy.instance = spyOn(HTMLElement.prototype, 'scrollIntoView')
}

/** A blame link resolves `focusStep` (the step's full hash) against the matching `step` entry's
    `id`, scrolls it into view centered — so the turn above it is visible, not just the marker
    pinned to the edge — opens its detail without hover, marks it, and moves focus there. */
export const FocusStepTargetsAndOpensStep: Story = {
  args: { focusStep: 's1' },
  decorators: [(StoryFn) => { armScrollIntoViewSpy(); return <StoryFn /> }],
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const marker = await waitFor(() => {
      const el = canvasElement.querySelector('[aria-current="location"]')
      expect(el).toBeInTheDocument()
      return el as HTMLElement
    })
    await expect(marker).toHaveAttribute('aria-label', expect.stringContaining('linked from blame'))
    await waitFor(() => expect(marker).toHaveFocus())
    await expect(scrollSpy.instance).toHaveBeenCalledWith(expect.objectContaining({ block: 'center' }))
    const detail = canvas.getByText(/tok/)
    await waitFor(() => expect(getComputedStyle(detail.parentElement!).opacity).toBe('1'))
  },
}

/** Without a `focusStep` nothing is targeted and nothing scrolls — the deep-link behavior never
    fires on a plain, unlinked visit to the transcript. */
export const FocusStepAbsentDoesNotScroll: Story = {
  decorators: [(StoryFn) => { armScrollIntoViewSpy(); return <StoryFn /> }],
  play: async ({ canvasElement }) => {
    await expect(canvasElement.querySelector('[aria-current="location"]')).not.toBeInTheDocument()
    await expect(scrollSpy.instance).not.toHaveBeenCalled()
  },
}

/** A `focusStep` that matches no captured step (e.g. a stale or foreign link) must not crash the
    transcript and must not target or scroll to anything. */
export const FocusStepNoMatchIsHarmless: Story = {
  args: { focusStep: 'not-a-real-step-hash' },
  decorators: [(StoryFn) => { armScrollIntoViewSpy(); return <StoryFn /> }],
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await expect(canvas.getByText('User')).toBeInTheDocument()
    await expect(canvasElement.querySelector('[aria-current="location"]')).not.toBeInTheDocument()
    await expect(scrollSpy.instance).not.toHaveBeenCalled()
  },
}

/** `focusStep` tolerates a shortened hash: a prefix of the step's full id still resolves, since
    Browse may link with a truncated display hash rather than the full 64 characters. */
export const FocusStepShortenedHashResolves: Story = {
  args: {
    focusStep: '9f2a7c4e1b3d',
    entries: [
      { type: 'user', id: 'z1', at: '09:00:00', content: 'Fix the boundary case.' },
      { type: 'step', id: '9f2a7c4e1b3d5608aa11bb22cc33dd44ee55ff660011223344556677889900aa', at: '09:00:05', hash: '9f2a7c4e', tree: 'e4b8a20', turn: 'turn-9', tokens: 512, files: 1 },
    ] satisfies TranscriptEntry[],
  },
  decorators: [(StoryFn) => { armScrollIntoViewSpy(); return <StoryFn /> }],
  play: async ({ canvasElement }) => {
    await waitFor(() => expect(canvasElement.querySelector('[aria-current="location"]')).toBeInTheDocument())
  },
}

/** The accent is a flash, not a permanent state: it draws the eye on arrival, then releases while
    the step stays open, focusable and announced. */
export const FocusStepHighlightReleases: Story = {
  args: { focusStep: 's1' },
  decorators: [(StoryFn) => { armScrollIntoViewSpy(); return <StoryFn /> }],
  play: async ({ canvasElement }) => {
    const chip = await waitFor(() => {
      // The id chip is a link (repoId is supplied by default in this file's meta args).
      const el = canvasElement.querySelector('[aria-current="location"] a')
      expect(el).toBeInTheDocument()
      return el as HTMLElement
    })
    await expect(chip.className).toContain('bg-accent-tint')
    // Outlast the flash, then confirm only the accent went away.
    await waitFor(() => expect(chip.className).not.toContain('bg-accent-tint'), { timeout: STEP_HIGHLIGHT_MS + 2000 })
    await expect(canvasElement.querySelector('[aria-current="location"]')).toBeInTheDocument()
  },
}

/** `repoId` (plus each step entry's full-hash `id`, see api/adapters.ts) threads down into
 *  StepMarker so both the step id and the tree value link into the file browser at that step. */
export const StepMarkerLinksToFilesAtStep: Story = {
  play: async ({ canvas }) => {
    // The fixture's step entry has id 's1' (its full hash) and display hash '7ac3ef1'.
    const idLink = canvas.getByRole('link', { name: 'Browse files at step 7ac3ef1' })
    await expect(idLink).toHaveAttribute('href', '/repos/demo-repo/files?step=s1')
    const treeLink = canvas.getByRole('link', { name: 'View the tree at step 7ac3ef1' })
    await expect(treeLink).toHaveAttribute('href', '/repos/demo-repo/files?step=s1')
  },
}

/** Without repoId there is nothing valid to link to, so the transcript's step markers fall back
 *  to plain text — no anchors anywhere, matching StepMarker's own fallback behaviour. */
export const NoLinksWithoutRepoId: Story = {
  args: { repoId: undefined },
  play: async ({ canvas, canvasElement }) => {
    await expect(canvas.getByText('7ac3ef1')).toBeInTheDocument()
    await expect(canvasElement.querySelectorAll('a')).toHaveLength(0)
  },
}
