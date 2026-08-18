import type { Conversation } from '../mocks/regent'

export type SessionRowProps = Conversation & { selected?: boolean; onClick?: () => void }

const agentMark: Record<string, string> = { Codex: 'CX', 'Claude Code': 'CL', OpenCode: 'OC' }

/** Dense conversation row for scanning large capture histories. */
export function SessionRow({ title, author = 'Unknown', agent = 'Legacy', model, branch, steps, files, relativeTime, status, selected = false, onClick }: SessionRowProps) {
  return <button type="button" onClick={onClick} aria-current={selected ? 'page' : undefined} className={`group relative grid min-h-12 w-full grid-cols-[25px_minmax(0,1fr)_auto] items-center gap-2 border-b border-line px-2.5 py-1.5 text-left transition-colors hover:bg-hover ${selected ? 'bg-hover-2' : 'bg-canvas'}`}>
    {selected && <span className="absolute inset-y-1.5 left-0 w-0.5 rounded-r bg-accent" />}
    <span className="flex size-5.5 items-center justify-center rounded-[4px] bg-field text-[8.5px] font-semibold text-ink-2 shadow-hairline">{agentMark[agent] ?? '?'}</span>
    <span className="min-w-0">
      <span className="flex items-center gap-1.5"><span className="truncate text-[11.5px] font-medium text-ink">{title}</span>{status === 'capturing' && <span className="size-1.5 shrink-0 rounded-full bg-green" title="Capturing" />}{status === 'legacy' && <span className="shrink-0 rounded-[3px] bg-field px-1 text-[8.5px] text-ink-3">legacy</span>}</span>
      <span className="mt-0.5 flex min-w-0 items-center gap-1.5 truncate text-[9.5px] text-ink-3"><span>{author}</span><span>·</span><span className="truncate font-mono">{branch}</span><span>·</span><span>{agent}</span>{model && <><span>·</span><span className="truncate">{model}</span></>}</span>
    </span>
    <span className="text-right text-[9.5px] tabular-nums text-ink-3"><span className="block">{relativeTime}</span><span className="mt-0.5 block">{steps}s · {files}f</span></span>
  </button>
}
