import type { Meta, StoryObj } from '@storybook/react-vite'
import { StepMarker } from './StepMarker'

const meta = { component: StepMarker, tags: ['ai-generated'], args: { hash: '7ac3ef1', tree: 'e4b8a20', turn: 'turn-184', tokens: 1842, files: 3, at: '13:05:09' } } satisfies Meta<typeof StepMarker>
export default meta
type Story = StoryObj<typeof meta>

export const CapturedTurn: Story = {}
export const LargeCapture: Story = { args: { hash: 'bd91c42', tree: '7fe206a', turn: 'turn-185', tokens: 42819, files: 27 } }
