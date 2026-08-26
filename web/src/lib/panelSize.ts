import { useCallback, useState } from 'react'

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value))

/** Persistent panel geometry shared by the workspace shell and its split views. */
export function usePersistentPanelSize(storageKey: string, defaultSize: number, min: number, max: number) {
  const [size, setSizeState] = useState(() => {
    try {
      const stored = Number(window.localStorage.getItem(`regent:panel:${storageKey}`))
      return Number.isFinite(stored) && stored > 0 ? clamp(stored, min, max) : defaultSize
    } catch {
      return defaultSize
    }
  })

  const setSize = useCallback((next: number) => {
    const clamped = clamp(Math.round(next), min, max)
    setSizeState(clamped)
    try { window.localStorage.setItem(`regent:panel:${storageKey}`, String(clamped)) } catch { /* storage can be unavailable */ }
  }, [storageKey, min, max])

  return [size, setSize] as const
}
