import { useEffect, useRef, useState } from 'react'
import type { TranscriptEntry } from '../api/types'
import { CodeBlock } from './CodeBlock'
import { StepMarker } from './StepMarker'
import { ThinkingReasoning } from './ThinkingReasoning'
import { ToolCallGroup } from './ToolCallGroup'

/** How long a deep-linked step stays visually flashed before releasing the accent. Long enough
 *  to find the step after the scroll settles, short enough not to sit there permanently. */
export const STEP_HIGHLIGHT_MS = 2600

export interface ConversationTranscriptProps { entries: TranscriptEntry[]; allOpen?: boolean; focusStep?: string; repoId?: string }

function toolGroupKey(entry: Extract<TranscriptEntry, { type: 'tools' }>) {
  const tools = new Set(entry.calls.map((call) => call.tool))
  if ([...tools].every((tool) => ['Read', 'Search'].includes(tool))) return 'Read'
  if (tools.size === 1) return entry.calls[0]?.tool || 'Tool'
  return [...tools].sort().join('+')
}

function mergeAdjacentTools(entries: TranscriptEntry[]): TranscriptEntry[] {
  return entries.reduce<TranscriptEntry[]>((merged, entry) => {
    const previous = merged.at(-1)
    if (entry.type === 'tools' && previous?.type === 'tools' && toolGroupKey(entry) === toolGroupKey(previous)) {
      merged[merged.length - 1] = {
        ...previous,
        id: `${previous.id}-${entry.id}`,
        at: entry.at || previous.at,
        calls: [...previous.calls, ...entry.calls],
        files: [...(previous.files || []), ...(entry.files || [])],
      }
      return merged
    }
    merged.push(entry)
    return merged
  }, [])
}

// Meta (speaker + timestamp) stays permanently in the a11y tree — only its opacity toggles — so
// screen-reader users always get it and hover/focus-within reveal never causes layout shift.
const messageMeta = 'mb-1 flex items-center gap-2 font-mono text-[10px] text-ink-3 opacity-0 transition-opacity duration-150 motion-reduce:transition-none group-hover:opacity-100 group-focus-within:opacity-100'

function Message({ label, at, children, user = false }: { label: string; at: string; children: React.ReactNode; user?: boolean }) {
  if (user) return <div className="flex justify-end px-6 py-4">
    <article className="group max-w-[min(720px,82%)]">
      <div className={`justify-end ${messageMeta}`}><span>{label}</span><time className="tabular-nums">{at}</time></div>
      <div className="rounded-[8px] border border-ink bg-ink px-3 py-2 text-[12.5px] leading-[1.6] text-page shadow-hairline">{children}</div>
    </article>
  </div>

  return <div className="flex justify-start px-6 py-4">
    <article className="group max-w-[min(860px,88%)]">
      <div className={messageMeta}><span>{label}</span><time className="tabular-nums">{at}</time></div>
      <div className="text-[12.5px] leading-[1.6] text-ink">{children}</div>
    </article>
  </div>
}

/** Canonical captured conversation with narration, reasoning, tools, and step boundaries in causal order. */
export function ConversationTranscript({ entries, allOpen, focusStep, repoId }: ConversationTranscriptProps) {
  const transcriptEntries = mergeAdjacentTools(entries)
  const targetRef = useRef<HTMLDivElement>(null)

  // A blame link passes the step's full hash. For `step` entries that hash lives in `id` — `hash`
  // is only the 8-char display prefix (see api/adapters.ts) — so match against `id`, and tolerate
  // a shortened focusStep by prefix so a truncated link still resolves to the right step.
  const targetId = focusStep
    ? transcriptEntries.find((entry) => entry.type === 'step' && entry.id.startsWith(focusStep))?.id
    : undefined

  // The flash is transient; `targeted` (open + focusable + announced) is not.
  const [highlighted, setHighlighted] = useState(false)
  useEffect(() => {
    if (!targetId) { setHighlighted(false); return }
    setHighlighted(true)
    const timer = window.setTimeout(() => setHighlighted(false), STEP_HIGHLIGHT_MS)
    return () => window.clearTimeout(timer)
  }, [targetId])

  useEffect(() => {
    if (!targetId || !targetRef.current) return
    // block: 'center' so the turn that produced the step is visible, not just the marker pinned
    // to the top edge. Reduced motion gets an instant jump — the global CSS reset only zeroes
    // animation-duration and doesn't cover a JS-driven smooth scroll.
    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    targetRef.current.scrollIntoView({ block: 'center', behavior: reduceMotion ? 'auto' : 'smooth' })
    targetRef.current.focus()
    // Depend only on targetId (derived, primitive) so a parent re-render with the same focusStep
    // and a fresh `entries` array reference does not re-trigger the scroll.
  }, [targetId])

  return <div className="min-w-0 bg-canvas py-5" aria-label="Full conversation transcript">
    {transcriptEntries.map((entry) => {
      if (entry.type === 'user') return <Message key={entry.id} label="User" at={entry.at} user><p className="m-0 text-page">{entry.content}</p></Message>
      if (entry.type === 'assistant') return <Message key={entry.id} label="Agent" at={entry.at}><p className="m-0">{entry.content}</p></Message>
      if (entry.type === 'reasoning') return <Message key={entry.id} label="Reason" at={entry.at}><ThinkingReasoning durationSeconds={entry.duration} lines={entry.lines} allOpen={allOpen} /></Message>
      if (entry.type === 'tools') return <div key={entry.id} className="px-6 py-2"><ToolCallGroup calls={entry.calls} files={entry.files} allOpen={allOpen} repoId={repoId} stepHash={entry.stepHash} /></div>
      if (entry.type === 'code') return <div key={entry.id} className="max-w-[920px] px-6 py-5"><div className="mb-3 flex items-center gap-2 text-[11.5px] text-ink-3"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden><path d="m4 17 6-5-6-5M12 19h8" /></svg><span>Viewed code</span><time className="font-mono text-[10px] tabular-nums">{entry.at}</time></div><CodeBlock filename={entry.filename} language={entry.language} code={entry.code} /></div>
      // `id` holds the step's full hash (see api/adapters.ts) — `hash` on the entry is only the
      // 8-char display prefix, so StepMarker needs `id` passed through separately as `fullHash`.
      return <StepMarker key={entry.id} {...entry} repoId={repoId} fullHash={entry.id} targeted={entry.id === targetId} highlighted={entry.id === targetId && highlighted} markerRef={entry.id === targetId ? targetRef : undefined} />
    })}
  </div>
}
