import type { Meta, StoryObj } from '@storybook/react-vite'
import { useState } from 'react'
import { expect, userEvent } from 'storybook/test'
import { ResizeHandle } from './ResizeHandle'

function ResizeDemo() {
  const [size, setSize] = useState(280)
  return <div className="flex h-72 bg-page text-ink"><div style={{ width: size }} className="shrink-0 bg-canvas p-4">Panel · {size}px</div><ResizeHandle label="Resize demo panel" value={size} min={180} max={480} defaultValue={280} onChange={setSize} /><div className="flex-1 p-4">Flexible panel</div></div>
}

const meta = { component: ResizeDemo, tags: ['ai-generated'], parameters: { layout: 'fullscreen' } } satisfies Meta<typeof ResizeDemo>
export default meta
type Story = StoryObj<typeof meta>

export const Default: Story = {
  play: async ({ canvas }) => {
    const handle = canvas.getByRole('separator', { name: 'Resize demo panel' })
    handle.focus()
    await userEvent.keyboard('{Shift>}{ArrowRight}{/Shift}')
    await expect(handle).toHaveAttribute('aria-valuenow', '320')
    await userEvent.keyboard('{Home}')
    await expect(handle).toHaveAttribute('aria-valuenow', '180')
  },
}
