import type { Meta, StoryObj } from '@storybook/react-vite'
import { CodeBlock } from './CodeBlock'

const meta = { component: CodeBlock, tags: ['ai-generated'], args: { filename: 'parser.ts', language: 'TypeScript', code: 'export function parseReminder(input: string) {\n  return parseNaturalDate(input)\n}' } } satisfies Meta<typeof CodeBlock>
export default meta
type Story = StoryObj<typeof meta>

export const Default: Story = {}
export const EmptyFile: Story = { args: { filename: 'empty.ts', code: '' } }
export const LongLine: Story = { args: { code: 'export const reminder = parseReminder(input, { timezone: user.timezone, locale: user.locale, preserveSource: true })' } }
