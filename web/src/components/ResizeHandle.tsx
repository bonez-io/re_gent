import { useRef, type KeyboardEvent, type PointerEvent } from 'react'

export interface ResizeHandleProps {
  label: string
  value: number
  min: number
  max: number
  defaultValue: number
  onChange: (value: number) => void
  className?: string
}

/** Keyboard- and pointer-accessible vertical separator for adjacent horizontal panels. */
export function ResizeHandle({ label, value, min, max, defaultValue, onChange, className = '' }: ResizeHandleProps) {
  const drag = useRef<{ pointerId: number; x: number; value: number } | undefined>(undefined)

  const start = (event: PointerEvent<HTMLDivElement>) => {
    drag.current = { pointerId: event.pointerId, x: event.clientX, value }
    event.currentTarget.setPointerCapture(event.pointerId)
  }
  const move = (event: PointerEvent<HTMLDivElement>) => {
    if (!drag.current || drag.current.pointerId !== event.pointerId) return
    onChange(drag.current.value + event.clientX - drag.current.x)
  }
  const stop = (event: PointerEvent<HTMLDivElement>) => {
    if (drag.current?.pointerId === event.pointerId) drag.current = undefined
  }
  const keyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const step = event.shiftKey ? 40 : 10
    if (event.key === 'ArrowLeft') { event.preventDefault(); onChange(value - step) }
    else if (event.key === 'ArrowRight') { event.preventDefault(); onChange(value + step) }
    else if (event.key === 'Home') { event.preventDefault(); onChange(min) }
    else if (event.key === 'End') { event.preventDefault(); onChange(max) }
  }

  return <div
    role="separator"
    aria-label={label}
    aria-orientation="vertical"
    aria-valuemin={min}
    aria-valuemax={max}
    aria-valuenow={value}
    tabIndex={0}
    onPointerDown={start}
    onPointerMove={move}
    onPointerUp={stop}
    onPointerCancel={stop}
    onDoubleClick={() => onChange(defaultValue)}
    onKeyDown={keyDown}
    title="Drag to resize · Double-click to reset"
    className={`group relative z-30 w-1 shrink-0 cursor-col-resize touch-none bg-line/60 outline-none transition-colors hover:bg-accent focus-visible:bg-accent ${className}`}
  >
    <span className="absolute inset-y-0 left-1/2 w-3 -translate-x-1/2" aria-hidden />
  </div>
}
