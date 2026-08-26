import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect } from 'storybook/test'
import { CodeView } from './CodeView'

const tsSample = `import { parseReminder } from './parser'

export function scheduleReminder(input: string, timezone: string) {
  const reminder = parseReminder(input, { timezone, preserveSource: true })
  return reminder.dueAt
}`

const goSample = `package reminders

func ParseReminder(input string, tz string) (*Reminder, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, err
	}
	return &Reminder{Input: input, Location: loc}, nil
}`

const jsonSample = `{
  "name": "re_gent",
  "version": "0.4.0",
  "private": true
}`

const tooLargeSample = Array.from({ length: 5200 }, (_, index) => `const line${index} = ${index}`).join('\n')
const binarySample = String.fromCharCode(0) + 'PK' + Array.from({ length: 200 }, (_, index) => String.fromCharCode(1 + (index % 6))).join('')

const meta = { component: CodeView, tags: ['ai-generated'], args: { path: 'src/reminders/parser.ts', code: tsSample }, parameters: { layout: 'fullscreen' } } satisfies Meta<typeof CodeView>
export default meta
type Story = StoryObj<typeof meta>

export const TypeScript: Story = {
  play: async ({ canvas }) => {
    // Highlighting is async — the keyword starts as plain text and upgrades in place.
    // Highlighting lazy-loads shiki's core, regex engine and grammar on first use,
    // so the default 1s matcher timeout is too tight under a full parallel suite.
    await expect(await canvas.findByText('function', undefined, { timeout: 15_000 })).toBeInTheDocument()
  },
}

export const Go: Story = { args: { path: 'reminders.go', code: goSample } }

export const Json: Story = { args: { path: 'package.json', code: jsonSample } }

export const UnknownExtension: Story = {
  args: { path: 'notes.xyz', code: 'just plain notes\nwith no grammar to speak of' },
  play: async ({ canvasElement, canvas }) => {
    await expect(canvas.getByText(/just plain notes/)).toBeInTheDocument()
    const root = canvasElement.querySelector('[data-state]')
    await expect(root).toHaveAttribute('data-state', 'plain')
  },
}

export const EmptyFile: Story = {
  args: { path: 'empty.ts', code: '' },
  play: async ({ canvasElement, canvas }) => {
    await expect(canvas.getByText('Empty file')).toBeInTheDocument()
    const root = canvasElement.querySelector('[data-state]')
    await expect(root).toHaveAttribute('data-state', 'empty')
  },
}

export const BinaryFile: Story = {
  args: { path: 'archive.bin', code: binarySample },
  play: async ({ canvasElement, canvas }) => {
    await expect(canvas.getByText(/Binary file/)).toBeInTheDocument()
    const root = canvasElement.querySelector('[data-state]')
    await expect(root).toHaveAttribute('data-state', 'binary')
  },
}

export const TooLarge: Story = {
  args: { path: 'huge.ts', code: tooLargeSample },
  play: async ({ canvasElement, canvas }) => {
    await expect(canvas.getByText(/showing first 500 lines/)).toBeInTheDocument()
    const root = canvasElement.querySelector('[data-state]')
    await expect(root).toHaveAttribute('data-state', 'too-large')
    await expect(canvas.queryByText('const line5199 = 5199')).not.toBeInTheDocument()
  },
}
