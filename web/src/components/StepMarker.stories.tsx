import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import { expect, waitFor, within } from 'storybook/test'
import { StepMarker } from './StepMarker'

// StepMarker renders a react-router Link whenever repoId/fullHash are supplied (the default here),
// so every story needs a router context even though most of them never navigate.
const withRouter = (StoryFn: () => React.ReactElement) => <MemoryRouter><StoryFn /></MemoryRouter>

const meta = {
  component: StepMarker,
  tags: ['ai-generated'],
  args: { hash: '7ac3ef1', tree: 'e4b8a20', turn: 'turn-184', tokens: 1842, files: 3, at: '13:05:09', repoId: 'demo-repo', fullHash: '7ac3ef1889900aabbccddeeff00112233445566778899aabbccddeeff00112' },
  decorators: [withRouter],
} satisfies Meta<typeof StepMarker>
export default meta
type Story = StoryObj<typeof meta>

export const CapturedTurn: Story = {}
export const LargeCapture: Story = { args: { hash: 'bd91c42', tree: '7fe206a', turn: 'turn-185', tokens: 42819, files: 27, fullHash: 'bd91c42889900aabbccddeeff00112233445566778899aabbccddeeff00112' } }

/** The step id sits centered on the hairline; tree/turn/tokens/files/time are the hover-only detail. */
export const ConciseByDefault: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await expect(canvas.getByText('7ac3ef1')).toBeInTheDocument()
    await expect(canvasElement.querySelector('[aria-label="Step 7ac3ef1"]')).toBeInTheDocument()
  },
}

/** Detail never unmounts — only its opacity toggles — so it stays in the a11y tree for
    screen-reader users even though it is visually hidden until hover or focus. */
export const DetailStaysInAccessibilityTree: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const detail = canvas.getByText(/tok/)
    await expect(detail).toBeInTheDocument()
    await expect(detail).not.toBeVisible()
    await expect(canvas.getByText('turn-184')).toBeInTheDocument()
  },
}

/** The reveal is not mouse-only: the id is a real focusable link, and focusing it (keyboard
    tab, no pointer involved) reveals the same detail via focus-within. */
export const DetailKeyboardReveal: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const link = canvas.getByRole('link', { name: 'Browse files at step 7ac3ef1' })
    const detail = canvas.getByText(/tok/)
    await expect(getComputedStyle(detail.parentElement!).opacity).toBe('0')
    link.focus()
    await expect(link).toHaveFocus()
    await waitFor(() => expect(getComputedStyle(detail.parentElement!).opacity).toBe('1'))
  },
}

/** `targeted` is the deep-link state a blame reference forces open — no hover or focus required.
    The marker gets `aria-current="location"` so assistive tech can tell it's the arrival point,
    and the detail row is visible immediately instead of waiting on a hover/focus reveal. */
export const TargetedFromBlameLink: Story = {
  args: { targeted: true, highlighted: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const marker = canvasElement.querySelector('[aria-current="location"]')
    await expect(marker).toBeInTheDocument()
    await expect(marker).toHaveAttribute('aria-label', 'Step 7ac3ef1, linked from blame')
    const detail = canvas.getByText(/tok/)
    await expect(getComputedStyle(detail.parentElement!).opacity).toBe('1')
  },
}

/** After the flash releases the step stays open and announced — only the accent goes away, so a
 *  step you already found does not keep shouting. */
export const TargetedAfterHighlightFades: Story = {
  args: { targeted: true, highlighted: false },
  play: async ({ canvas }) => {
    // Both the wrapper and the chip announce the blame link, so query the chip by role.
    const chip = canvas.getByRole('link', { name: /linked from blame/ })
    await expect(chip).toBeInTheDocument()
    await expect(chip.className).not.toContain('bg-accent-tint')
  },
}

/** The step id and the tree value both open the file browser at this step's full hash — the
 *  8-char `hash` display prefix is never enough on its own, so `fullHash` carries the real url. */
export const LinksToFilesAtStep: Story = {
  play: async ({ canvas }) => {
    const idLink = canvas.getByRole('link', { name: 'Browse files at step 7ac3ef1' })
    const expectedHref = '/repos/demo-repo/files?step=7ac3ef1889900aabbccddeeff00112233445566778899aabbccddeeff00112'
    await expect(idLink).toHaveAttribute('href', expectedHref)
    // Two links share the destination (id chip + tree value) — both must resolve to it.
    const treeLink = canvas.getByRole('link', { name: 'View the tree at step 7ac3ef1' })
    await expect(treeLink).toHaveAttribute('href', expectedHref)
    await expect(canvas.getAllByRole('link')).toHaveLength(2)
  },
}

/** repoId is percent-encoded like any other path/query segment used for in-app navigation. */
export const RepoIdIsEncoded: Story = {
  args: { repoId: 'demo repo/staging' },
  play: async ({ canvas }) => {
    const idLink = canvas.getByRole('link', { name: 'Browse files at step 7ac3ef1' })
    await expect(idLink.getAttribute('href')).toMatch(/^\/repos\/demo%20repo%2Fstaging\/files\?step=/)
  },
}

/** Without repoId/fullHash there is no valid destination, so the id and tree render as plain text
 *  rather than a link to nowhere — the component never guesses a URL it wasn't given. */
export const PlainTextWithoutRepoId: Story = {
  args: { repoId: undefined, fullHash: undefined },
  play: async ({ canvas, canvasElement }) => {
    await expect(canvas.getByText('7ac3ef1')).toBeInTheDocument()
    await expect(canvasElement.querySelectorAll('a')).toHaveLength(0)
    await expect(canvasElement.querySelector('[aria-label="Step 7ac3ef1 details"]')).toBeInTheDocument()
  },
}
