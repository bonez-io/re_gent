import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import type { LogCause, LogStep } from '../api/types'

export interface StepDetailPanelProps {
  step?: LogStep
  isPending?: boolean
  error?: Error
  onClose: () => void
  repoId?: string
}

const DASH = '—'
const PREVIEW_CHARS = 600
const EXPANDED_CAP = 20000

function formatTimestamp(value?: string): string {
  if (!value) return 'not recorded'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return 'not recorded'
  return date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })
}

function formatTokens(value?: number): string | null {
  if (typeof value !== 'number' || Number.isNaN(value)) return null
  const abs = Math.abs(value)
  if (abs < 1000) return String(value)
  if (abs < 1_000_000) return `${(value / 1000).toFixed(value % 1000 === 0 ? 0 : 1)}K`
  return `${(value / 1_000_000).toFixed(1)}M`
}

function truncateHash(hash: string, front = 10, back = 6): string {
  if (hash.length <= front + back + 1) return hash
  return `${hash.slice(0, front)}…${hash.slice(-back)}`
}

// The file browser at a given step is always addressed by that step's own hash — a step's
// "tree" and a parent's tree are both just "the file browser as of step X", so both destinations
// route through this same shape.
const filesHref = (repoId: string, stepHash: string) => `/repos/${encodeURIComponent(repoId)}/files?step=${encodeURIComponent(stepHash)}`
const fileAtStepHref = (repoId: string, stepHash: string, path: string) => `${filesHref(repoId, stepHash)}&path=${encodeURIComponent(path)}`
const sessionHref = (repoId: string, sessionId: string, stepHash: string) => `/repos/${encodeURIComponent(repoId)}/sessions/${encodeURIComponent(sessionId)}?step=${encodeURIComponent(stepHash)}`

const linkClassName = 'text-ink-2 transition-colors hover:text-ink hover:underline focus-visible:text-ink focus-visible:underline'

/** Serializes any tool payload to readable text, guarding against circular refs and unserializable values. */
function stringifyValue(value: unknown): string {
  if (value === undefined) return ''
  if (value === null) return 'null'
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  const seen = new WeakSet<object>()
  try {
    const text = JSON.stringify(value, (_key, val) => {
      if (typeof val === 'bigint') return `${val.toString()}n`
      if (typeof val === 'function') return '[Function]'
      if (typeof val === 'object' && val !== null) {
        if (seen.has(val)) return '[Circular]'
        seen.add(val)
      }
      return val
    }, 2)
    return text ?? ''
  } catch {
    try {
      return String(value)
    } catch {
      return '[Unserializable value]'
    }
  }
}

function hasValue(value: unknown): boolean {
  if (value === undefined || value === null) return false
  if (typeof value === 'string') return value.length > 0
  if (Array.isArray(value)) return value.length > 0
  if (typeof value === 'object') return Object.keys(value).length > 0
  return true
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return <div className="grid grid-cols-[126px_minmax(0,1fr)] gap-2 px-3 py-1.5">
    <span className="text-[11px] leading-4 text-ink-3">{label}</span>
    <span className="min-w-0 break-all text-[11.5px] leading-4 text-ink-2">{children}</span>
  </div>
}

function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      /* clipboard unavailable in this environment */
    }
  }
  return <button type="button" aria-label={label} onClick={copy} className={`shrink-0 rounded-[3px] border border-line px-1.5 py-0.5 text-[10px] font-medium shadow-hairline transition-colors ${copied ? 'bg-accent-tint text-accent-ink' : 'bg-field text-ink-2 hover:bg-hover'}`}>{copied ? 'Copied' : 'Copy'}</button>
}

/** Truncated payload viewer: never dumps a huge string into the DOM eagerly. */
function Payload({ value }: { value: unknown }) {
  const [expanded, setExpanded] = useState(false)
  const text = stringifyValue(value)
  if (!text) return <span className="text-ink-3">not recorded</span>
  const isLong = text.length > PREVIEW_CHARS
  const shown = expanded ? text.slice(0, EXPANDED_CAP) : text.slice(0, PREVIEW_CHARS)
  const stillCapped = expanded && text.length > EXPANDED_CAP
  return <div className="min-w-0">
    <pre className="m-0 max-h-[280px] overflow-auto whitespace-pre-wrap break-all rounded-[4px] border border-line bg-inset px-2 py-1.5 font-mono text-[11px] leading-[1.5] text-ink-2">{shown}{!expanded && isLong ? '…' : ''}</pre>
    {isLong && <button type="button" onClick={() => setExpanded((value) => !value)} className="mt-1 text-[10.5px] font-medium text-accent-ink hover:underline">{expanded ? 'Show less' : `Show more (${text.length.toLocaleString()} chars)`}</button>}
    {stillCapped && <p className="m-0 mt-1 text-[10px] text-ink-3">Showing first {EXPANDED_CAP.toLocaleString()} of {text.length.toLocaleString()} characters.</p>}
  </div>
}

function CauseRow({ cause }: { cause: LogCause }) {
  const [open, setOpen] = useState(false)
  return <div className="overflow-hidden rounded-[4px] border border-line bg-canvas">
    <button type="button" aria-expanded={open} onClick={() => setOpen((value) => !value)} className="flex w-full items-center justify-between gap-2 px-2.5 py-1.5 text-left hover:bg-hover">
      <span className="flex min-w-0 items-baseline gap-2">
        <span className="text-[11.5px] font-medium text-ink-2">{cause.tool || 'not recorded'}</span>
        <span className="min-w-0 truncate font-mono text-[10.5px] text-ink-3">{cause.tool_use_id || DASH}</span>
      </span>
      <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" className="shrink-0 text-ink-3 transition-transform duration-200" style={{ transform: open ? 'rotate(0deg)' : 'rotate(-90deg)' }} aria-hidden="true"><path d="M6 9l6 6 6-6" /></svg>
    </button>
    {open && <div className="flex flex-col gap-2 border-t border-line px-2.5 py-2">
      <div><div className="mb-1 font-mono text-[10px] uppercase tracking-[0.06em] text-ink-3">Arguments</div><Payload value={cause.args} /></div>
      <div><div className="mb-1 font-mono text-[10px] uppercase tracking-[0.06em] text-ink-3">Result</div><Payload value={cause.result} /></div>
    </div>}
  </div>
}

/**
 * Bottom-docked panel showing the full recorded context of one re_gent step: identity, provenance,
 * usage, tool causes, touched files, and uncompensated effects. Purely presentational — the caller
 * owns fetching `step` and supplies loading/error state directly.
 */
export function StepDetailPanel({ step, isPending, error, onClose, repoId }: StepDetailPanelProps) {
  const panelRef = useRef<HTMLDivElement>(null)
  const closeButtonRef = useRef<HTMLButtonElement>(null)
  const previouslyFocused = useRef<HTMLElement | null>(null)

  useEffect(() => {
    previouslyFocused.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    closeButtonRef.current?.focus()
    return () => {
      previouslyFocused.current?.focus()
    }
  }, [])

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation()
        onClose()
        return
      }
      if (event.key !== 'Tab' || !panelRef.current) return
      const focusable = Array.from(panelRef.current.querySelectorAll<HTMLElement>('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')).filter((element) => !element.hasAttribute('disabled'))
      if (focusable.length === 0) return
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  if (!step && !isPending && !error) return null

  const titleId = 'step-detail-panel-title'
  const titleText = step ? `Step ${truncateHash(step.hash || DASH)}` : isPending ? 'Loading step' : 'Step details'

  const causes: LogCause[] = step
    ? step.causes && step.causes.length > 0
      ? step.causes
      : step.tool
        ? [{ tool: step.tool, tool_use_id: step.tool_use_id, args: step.args, result: step.result }]
        : []
    : []

  const usageEntries: Array<[string, number | undefined]> = step?.usage
    ? [
        ['Input tokens', step.usage.input_tokens],
        ['Output tokens', step.usage.output_tokens],
        ['Cache read tokens', step.usage.cache_read_tokens],
      ]
    : []
  const usageRows = usageEntries.filter((entry): entry is [string, number] => typeof entry[1] === 'number' && !Number.isNaN(entry[1]))

  const files = step?.files?.filter((path) => typeof path === 'string' && path.length > 0) ?? []
  const effects = step?.effects?.filter(hasValue) ?? []
  const authorText = step?.author?.name || step?.author?.email ? [step?.author?.name, step?.author?.email].filter(Boolean).join(' · ') : DASH

  return <div ref={panelRef} role="dialog" aria-modal="false" aria-labelledby={titleId} className="fixed inset-x-0 bottom-0 z-40 flex max-h-[46vh] flex-col border-t border-line bg-canvas shadow-overlay">
    <header className="flex shrink-0 items-center justify-between gap-3 border-b border-line px-4 py-2.5">
      <div className="flex min-w-0 items-baseline gap-2">
        <span className="regent-kicker">Step</span>
        <h2 id={titleId} className="m-0 min-w-0 truncate font-mono text-[13px] font-semibold text-ink">{titleText}</h2>
        {step?.tool && <span className="shrink-0 rounded-[3px] border border-line bg-field px-1.5 py-0.5 text-[10px] text-ink-3">{step.tool}</span>}
      </div>
      <button ref={closeButtonRef} type="button" aria-label="Close step details" onClick={onClose} className="flex size-7 shrink-0 items-center justify-center rounded-[4px] text-ink-3 hover:bg-field hover:text-ink">×</button>
    </header>

    <div className="min-h-0 flex-1 overflow-y-auto">
      {isPending && <div className="px-4 py-8 text-center text-[12px] text-ink-3">Loading step…</div>}
      {!isPending && error && <div className="px-4 py-8 text-center text-[12px] text-red">Failed to load step{error.message ? `: ${error.message}` : '.'}</div>}
      {!isPending && !error && step && <div className="divide-y divide-line">
        <section>
          <div className="px-3 pt-2.5 pb-1 font-mono text-[10px] uppercase tracking-[0.06em] text-ink-3">Identity</div>
          <Row label="Hash">
            <span className="inline-flex min-w-0 items-center gap-1.5">
              <span className="min-w-0 truncate" title={step.hash || undefined}>{step.hash || DASH}</span>
              {step.hash && <CopyButton value={step.hash} label="Copy step hash" />}
            </span>
          </Row>
          <Row label="Parent">{repoId && step.parent
            ? <Link to={filesHref(repoId, step.parent)} title={step.parent} aria-label={`Browse files at step ${truncateHash(step.parent)}`} className={linkClassName}>{step.parent}</Link>
            : <span title={step.parent || undefined}>{step.parent || DASH}</span>}</Row>
          <Row label="Tree">{repoId && step.tree && step.hash
            ? <Link to={filesHref(repoId, step.hash)} title={step.tree} aria-label={`Browse files at step ${truncateHash(step.hash)}`} className={linkClassName}>{step.tree}</Link>
            : <span title={step.tree || undefined}>{step.tree || DASH}</span>}</Row>
          <Row label="Session">{repoId && step.session_id && step.hash
            ? <Link to={sessionHref(repoId, step.session_id, step.hash)} title={step.session_id} aria-label={`View session ${step.session_id}, highlighting step ${truncateHash(step.hash)}`} className={linkClassName}>{step.session_id}</Link>
            : <span title={step.session_id || undefined}>{step.session_id || DASH}</span>}</Row>
          <Row label="Turn / tool call"><span title={step.tool_use_id || undefined}>{step.tool_use_id || DASH}</span></Row>
        </section>

        <section>
          <div className="px-3 pt-2.5 pb-1 font-mono text-[10px] uppercase tracking-[0.06em] text-ink-3">Provenance</div>
          <Row label="Timestamp">{formatTimestamp(step.timestamp)}</Row>
          <Row label="Origin">{step.origin || DASH}</Row>
          <Row label="Author">{authorText}</Row>
        </section>

        {usageRows.length > 0 && <section>
          <div className="px-3 pt-2.5 pb-1 font-mono text-[10px] uppercase tracking-[0.06em] text-ink-3">Usage</div>
          {usageRows.map(([label, value]) => <Row key={label} label={label}>{formatTokens(value) ?? DASH}</Row>)}
        </section>}

        {causes.length > 0 && <section className="px-3 py-2.5">
          <div className="mb-1.5 font-mono text-[10px] uppercase tracking-[0.06em] text-ink-3">Causes</div>
          <div className="flex flex-col gap-1.5">{causes.map((cause, index) => <CauseRow key={`${index}-${cause.tool_use_id || cause.tool}`} cause={cause} />)}</div>
        </section>}

        {files.length > 0 && <section className="px-3 py-2.5">
          <div className="mb-1.5 font-mono text-[10px] uppercase tracking-[0.06em] text-ink-3">Files</div>
          <div className="flex flex-wrap gap-1.5">{files.map((path) => repoId && step.hash
            ? <Link key={path} to={fileAtStepHref(repoId, step.hash, path)} aria-label={`View ${path} at step ${truncateHash(step.hash)}`} className="inline-flex max-w-full items-center rounded-[3px] border border-line bg-field px-1.5 py-0.5 font-mono text-[10.5px] text-ink-2 shadow-hairline transition-colors hover:border-ink-3/60 hover:text-ink hover:bg-hover focus-visible:border-ink-3/60 focus-visible:text-ink"><span className="truncate">{path}</span></Link>
            : <span key={path} className="inline-flex max-w-full items-center rounded-[3px] border border-line bg-field px-1.5 py-0.5 font-mono text-[10.5px] text-ink-2 shadow-hairline"><span className="truncate">{path}</span></span>)}</div>
        </section>}

        {effects.length > 0 && <section className="px-3 py-2.5">
          <div className="mb-1.5 font-mono text-[10px] uppercase tracking-[0.06em] text-ink-3">Effects</div>
          <div className="flex flex-col gap-1.5">{effects.map((effect, index) => <div key={index} className="rounded-[4px] border border-line bg-canvas px-2.5 py-2"><Payload value={effect} /></div>)}</div>
        </section>}
      </div>}
    </div>
  </div>
}
