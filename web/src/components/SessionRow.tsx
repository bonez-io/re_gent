export interface SessionRowProps { title: string; author?: string; agent?: string; model?: string; steps: number; relativeTime: string; selected?: boolean }

/** Flat history row; its density follows Entire's session explorer hierarchy. */
export function SessionRow({ title, author = 'Unknown', agent = 'Unknown agent', model, steps, relativeTime, selected = false }: SessionRowProps) {
  const initials = author === 'Unknown' ? '?' : author.split(/\s+/).map((part) => part[0]).join('').slice(0, 2).toUpperCase()
  return <button type="button" aria-current={selected ? 'page' : undefined} className={`grid w-full grid-cols-[36px_minmax(0,1fr)_auto] items-center gap-3 border-b border-line px-3 py-3 text-left transition-colors hover:bg-hover ${selected ? 'bg-accent-tint' : 'bg-surface'}`}>
    <span className="flex size-8 items-center justify-center rounded-[9px] bg-field text-[11px] font-semibold text-ink-2 shadow-hairline">{initials}</span>
    <span className="min-w-0"><span className="block truncate text-[13px] font-semibold text-ink">{title}</span><span className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11.5px] text-ink-3"><span>{author} · {relativeTime}</span><span className="rounded-[5px] bg-field px-1.5 py-0.5 text-ink-2 shadow-hairline">{agent}</span>{model && <span className="font-mono">{model}</span>}</span></span>
    <span className="flex items-center gap-2 text-[11.5px] text-ink-3"><span>{steps} steps</span><span aria-hidden>›</span></span>
  </button>
}
