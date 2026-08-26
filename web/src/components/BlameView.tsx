import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { BlameResponse } from '../api/types'
import { languageForPath, useCodeTokens } from '../lib/highlight'
import { AgentIcon, agentColor, agentLabel } from './AgentIcon'
import { CodeLine, LARGE_FILE_PREVIEW_LINES } from './CodeView'
import { StepDetailPanel } from './StepDetailPanel'

/** Git-length prefix: short enough for a narrow gutter, long enough to match against the Steps view. */
const SHORT = 7
const short = (hash?: string) => (hash ? hash.slice(0, SHORT) : '')

type BlameLine = BlameResponse['lines'][number]
interface Row { line: BlameLine; blockStart: boolean }

/** Groups consecutive lines sharing a step, the way a blame gutter reads: attribution
 *  is stated once per run rather than repeated on every line. */
function toRows(lines: BlameLine[]): Row[] {
  let previous: string | undefined
  return lines.map((line, index) => {
    const blockStart = index === 0 || line.step_hash !== previous
    previous = line.step_hash
    return { line, blockStart }
  })
}

const describe = (line: BlameLine) =>
  line.step_hash ? `Step ${short(line.step_hash)} by ${agentLabel(line.origin)}` : 'Line with no recorded step'

export interface BlameViewProps {
  repoId: string
  data: BlameResponse
}

/** A file at one step: syntax-highlighted source beside per-line provenance, where any
 *  attributed run opens the step that produced it. */
export function BlameView({ repoId, data }: BlameViewProps) {
  const [openHash, setOpenHash] = useState<string>()
  const navigate = useNavigate()

  const rows = useMemo(() => toRows(data.lines), [data.lines])
  const code = useMemo(() => data.lines.map((line) => line.content).join('\n'), [data.lines])
  const language = useMemo(() => languageForPath(data.path), [data.path])
  const { lines: tokens, state } = useCodeTokens(code, language)
  const visibleRows = state === 'too-large' ? rows.slice(0, LARGE_FILE_PREVIEW_LINES) : rows
  const isTruncated = visibleRows.length < rows.length

  // Only fetches once a run is actually opened. Blame knows the step but not which session
  // produced it, so the step has to resolve before we can link into its transcript.
  const step = useQuery({ queryKey: ['step', repoId, openHash], queryFn: () => api.step(repoId, openHash!), enabled: Boolean(openHash), retry: false })
  const stepSession = step.data?.session_id
  useEffect(() => {
    if (!openHash || !stepSession) return
    navigate(`/repos/${encodeURIComponent(repoId)}/sessions/${encodeURIComponent(stepSession)}?step=${encodeURIComponent(openHash)}`)
  }, [openHash, stepSession, navigate, repoId])

  // Icon, step id, line number, code — the gutter is always shown, no collapse toggle.
  const columns = 'grid-cols-[14px_42px_26px_minmax(0,1fr)]'

  return <div className="flex min-h-0 flex-1 flex-col">
    {state === 'binary' ? <p className="m-0 px-3 py-4 text-[11.5px] text-ink-3">Binary file — preview not available.</p>
      : state === 'empty' ? <p className="m-0 px-3 py-4 text-[11.5px] text-ink-3">This file is empty at this step.</p>
        : <div className="min-h-0 flex-1 overflow-auto py-0.5 font-mono text-[11px] leading-[17px]">
          {state === 'too-large' && <p className="m-0 px-3 py-2 font-sans text-[11.5px] leading-4 text-ink-3">File too large to highlight ({rows.length.toLocaleString()} lines) — {isTruncated ? `showing first ${visibleRows.length.toLocaleString()} lines` : 'showing plain text'}.</p>}
          {visibleRows.map(({ line, blockStart }, index) => {
            const label = describe(line)
            const openable = blockStart && Boolean(line.step_hash)
            return <div key={line.number}
              className={`grid ${columns} min-w-max items-center border-t hover:bg-hover ${blockStart && index !== 0 ? 'border-line' : 'border-transparent'}`}>
              <span className="flex items-center justify-center" style={blockStart ? { color: agentColor(line.origin) } : undefined}>
                {blockStart && <AgentIcon origin={line.origin} decorative className="size-3.5" />}
              </span>

              {/* aria-label carries the vendor name explicitly — the tinted icon beside it is decorative,
                  so colour is never the only way to tell whose run this is. */}
              {openable
                ? <button type="button" onClick={() => setOpenHash(line.step_hash)} title={label} aria-label={label}
                  className="text-left font-mono text-[10px] text-ink-3 transition-colors hover:text-ink focus-visible:text-ink">{short(line.step_hash)}</button>
                : <span className="font-mono text-[10px] text-ink-3">{blockStart ? '—' : ''}</span>}

              <span className="select-none pr-1.5 text-right text-ink-3">{line.number}</span>
              <CodeLine tokens={tokens?.[index]} text={line.content} className="pl-1.5" />
            </div>
          })}
        </div>}

    {/* Resolving hands off to the session transcript. The panel is the honest fallback for a
        step that records no session, or that fails to load — never a silent dead click. */}
    {openHash && !stepSession && <StepDetailPanel step={step.data} isPending={step.isPending} error={step.error ?? undefined} onClose={() => setOpenHash(undefined)} />}
  </div>
}
