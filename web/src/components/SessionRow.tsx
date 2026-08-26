import type { Conversation } from '../api/types'
import { AgentIcon, agentColor } from './AgentIcon'

export type SessionRowProps = Conversation & { selected?: boolean; onClick?: () => void }

/** Rows carry either a display name ('Claude Code') or a raw origin ('claude_code'); the
 *  icon set is keyed by origin, so normalize before looking a vendor up. */
const originOf = (agent: string) => agent.trim().toLowerCase().replace(/\s+/g, '_')

/** Dense conversation row for scanning large capture histories. */
export function SessionRow({ title, author = 'Unknown', agent = 'Legacy', model, branch, steps, files, relativeTime, status, selected = false, onClick }: SessionRowProps) {
  return <button type="button" onClick={onClick} aria-current={selected ? 'page' : undefined} className={`group relative grid min-h-12 w-full grid-cols-[26px_minmax(0,1fr)_auto] items-center gap-2 border-b border-line px-2.5 py-1 text-left transition-[background-color,transform] duration-150 last:border-b-0 hover:bg-hover active:scale-[0.998] ${selected ? 'bg-hover-2' : 'bg-canvas'}`}>
    {selected && <span className="absolute inset-y-1.5 left-0 w-0.5 bg-accent" />}
    <span className={`flex size-6 items-center justify-center rounded-[3px] border shadow-hairline ${selected ? 'border-ink' : 'border-line'}`} style={{ color: agentColor(originOf(agent)) }}><AgentIcon origin={originOf(agent)} decorative className="size-3.5" /></span>
    <span className="min-w-0">
      <span className="flex items-center gap-1"><span className="truncate text-[12.5px] font-medium leading-4 text-ink">{title}</span>{status === 'capturing' && <span className="size-1.5 shrink-0 rounded-full bg-green" title="Capturing" />}{status === 'legacy' && <span className="shrink-0 rounded-[2px] border border-line bg-field px-1 font-mono text-[9px] text-ink-3">legacy</span>}</span>
      <span className="flex min-w-0 items-center gap-1 truncate text-[10.5px] leading-4 text-ink-3"><span>{author}</span><span>·</span><span className="truncate font-mono">{branch}</span><span>·</span><span>{agent}</span>{model && <><span>·</span><span className="truncate">{model}</span></>}</span>
    </span>
    <span className="text-right text-[10.5px] leading-4 tabular-nums text-ink-3"><span className="block">{relativeTime}</span><span className="block">{steps}s · {files}f</span></span>
  </button>
}
