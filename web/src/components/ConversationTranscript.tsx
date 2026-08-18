import type { TranscriptEntry } from '../mocks/regent'
import { CodeBlock } from './CodeBlock'
import { StepMarker } from './StepMarker'
import { ThinkingReasoning } from './ThinkingReasoning'
import { ToolCallGroup } from './ToolCallGroup'

export interface ConversationTranscriptProps { entries: TranscriptEntry[] }

function Message({ label, at, children, user = false }: { label: string; at: string; children: React.ReactNode; user?: boolean }) {
  return <div className={`grid grid-cols-[54px_minmax(0,1fr)_48px] gap-2 border-b border-line px-3 py-2.5 ${user ? 'bg-hover/40' : ''}`}>
    <div className="pt-0.5 text-[9.5px] font-medium text-ink-3">{label}</div>
    <div className="min-w-0 text-[11.5px] leading-[1.55] text-ink-2">{children}</div>
    <time className="pt-0.5 text-right text-[9px] tabular-nums text-ink-3">{at}</time>
  </div>
}

/** Canonical captured conversation with narration, reasoning, tools, and step boundaries in causal order. */
export function ConversationTranscript({ entries }: ConversationTranscriptProps) {
  return <div className="min-w-0 bg-canvas" aria-label="Full conversation transcript">
    {entries.map((entry) => {
      if (entry.type === 'user') return <Message key={entry.id} label="User" at={entry.at} user><p className="m-0 text-ink">{entry.content}</p></Message>
      if (entry.type === 'assistant') return <Message key={entry.id} label="Agent" at={entry.at}><p className="m-0">{entry.content}</p></Message>
      if (entry.type === 'reasoning') return <Message key={entry.id} label="Reason" at={entry.at}><ThinkingReasoning durationSeconds={entry.duration} lines={entry.lines} /></Message>
      if (entry.type === 'tools') return <Message key={entry.id} label="Tools" at={entry.at}><ToolCallGroup calls={entry.calls} files={entry.files} /></Message>
      if (entry.type === 'code') return <Message key={entry.id} label="Code" at={entry.at}><CodeBlock filename={entry.filename} language={entry.language} code={entry.code} /></Message>
      return <StepMarker key={entry.id} {...entry} />
    })}
  </div>
}
