import type { TranscriptEntry } from '../api/types'
import { CodeBlock } from './CodeBlock'
import { StepMarker } from './StepMarker'
import { ThinkingReasoning } from './ThinkingReasoning'
import { ToolCallGroup } from './ToolCallGroup'

export interface ConversationTranscriptProps { entries: TranscriptEntry[]; allOpen?: boolean }

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

function Message({ label, at, children, user = false }: { label: string; at: string; children: React.ReactNode; user?: boolean }) {
  if (user) return <div className="flex justify-end px-6 py-4">
    <article className="max-w-[min(720px,82%)]">
      <div className="mb-1 flex items-center justify-end gap-2 font-mono text-[10px] text-ink-3"><span>{label}</span><time className="tabular-nums">{at}</time></div>
      <div className="rounded-[8px] border border-ink bg-ink px-3 py-2 text-[12.5px] leading-[1.55] text-page shadow-hairline">{children}</div>
    </article>
  </div>

  return <div className="flex justify-start px-6 py-4">
    <article className="max-w-[min(860px,88%)]">
      <div className="mb-1 flex items-center gap-2 font-mono text-[10px] text-ink-3"><span>{label}</span><time className="tabular-nums">{at}</time></div>
      <div className="text-[15px] leading-7 text-ink">{children}</div>
    </article>
  </div>
}

/** Canonical captured conversation with narration, reasoning, tools, and step boundaries in causal order. */
export function ConversationTranscript({ entries, allOpen }: ConversationTranscriptProps) {
  const transcriptEntries = mergeAdjacentTools(entries)
  return <div className="min-w-0 bg-canvas py-5" aria-label="Full conversation transcript">
    {transcriptEntries.map((entry) => {
      if (entry.type === 'user') return <Message key={entry.id} label="User" at={entry.at} user><p className="m-0 text-page">{entry.content}</p></Message>
      if (entry.type === 'assistant') return <Message key={entry.id} label="Agent" at={entry.at}><p className="m-0">{entry.content}</p></Message>
      if (entry.type === 'reasoning') return <Message key={entry.id} label="Reason" at={entry.at}><ThinkingReasoning durationSeconds={entry.duration} lines={entry.lines} allOpen={allOpen} /></Message>
      if (entry.type === 'tools') return <div key={entry.id} className="px-6 py-2"><ToolCallGroup calls={entry.calls} files={entry.files} allOpen={allOpen} /></div>
      if (entry.type === 'code') return <div key={entry.id} className="max-w-[920px] px-6 py-5"><div className="mb-3 flex items-center gap-2 text-[15px] text-ink-3"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden><path d="m4 17 6-5-6-5M12 19h8" /></svg><span>Viewed code</span><time className="font-mono text-[10px] tabular-nums">{entry.at}</time></div><CodeBlock filename={entry.filename} language={entry.language} code={entry.code} /></div>
      return <StepMarker key={entry.id} {...entry} />
    })}
  </div>
}
