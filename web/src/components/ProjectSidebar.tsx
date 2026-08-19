import { useState } from 'react'

export type RegentView = 'sessions' | 'team' | 'steps' | 'files' | 'sync'

const items: { key: RegentView; label: string; shortcut?: string }[] = [
  { key: 'sessions', label: 'Sessions', shortcut: '7' },
  { key: 'team', label: 'Team' },
  { key: 'steps', label: 'Steps' },
  { key: 'files', label: 'Files & blame' },
  { key: 'sync', label: 'Sync status' },
]

function NavIcon({ kind }: { kind: RegentView }) {
  const paths: Record<RegentView, React.ReactNode> = {
    sessions: <><path d="M5 6.5h14M5 11.5h14M5 16.5h9" /><circle cx="3" cy="6.5" r=".5" fill="currentColor" /><circle cx="3" cy="11.5" r=".5" fill="currentColor" /><circle cx="3" cy="16.5" r=".5" fill="currentColor" /></>,
    team: <><circle cx="9" cy="8" r="3" /><path d="M3.5 19a5.5 5.5 0 0 1 11 0" /><circle cx="17" cy="9" r="2.2" /><path d="M15.5 14.5A4.5 4.5 0 0 1 21 19" /></>,
    steps: <><circle cx="6" cy="6" r="2" /><circle cx="18" cy="18" r="2" /><path d="M8 6h3a3 3 0 0 1 3 3v6a3 3 0 0 0 3 3M6 8v10h3" /></>,
    files: <><path d="M4 6h6l2-2h8v16H4zM4 9h16" /></>,
    sync: <><path d="M19 7a8 8 0 0 0-13.7-1L3 8M5 17a8 8 0 0 0 13.7 1l2.3-2" /><path d="M3 4v4h4M21 20v-4h-4" /></>,
  }
  return <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">{paths[kind]}</svg>
}

export interface ProjectSidebarProps {
  project?: string
  deployment?: string
  conversationCount?: number
  active?: RegentView
  onNavigate?: (view: RegentView) => void
  onProjectClick?: () => void
}

/** Compact project navigation, adapted from Beautiful UI's MIT sidebar primitive. */
export function ProjectSidebar({ project = 'girlfriend-assistant', deployment = 'Self-hosted · local', conversationCount = 7, active: controlled, onNavigate, onProjectClick }: ProjectSidebarProps) {
  const [localActive, setLocalActive] = useState<RegentView>('sessions')
  const active = controlled ?? localActive
  const navigate = (view: RegentView) => { setLocalActive(view); onNavigate?.(view) }

  return <aside className="flex h-full min-h-screen w-48 flex-col border-r border-line bg-canvas" aria-label="Project navigation">
    <div className="flex h-11 items-center border-b border-line px-3">
      <a href="https://www.re-gent.dev/" className="flex size-7 items-center justify-center rounded-[3px] transition-[transform,opacity] duration-150 hover:opacity-80 active:scale-95" aria-label="Visit re_gent"><img src="/favicon.svg" alt="" className="block size-7" /></a>
      <button className="ml-auto rounded-[3px] px-1 font-mono text-[15px] text-ink-3 transition-colors duration-150 hover:bg-hover hover:text-ink" aria-label="Project menu">•••</button>
    </div>
    <div className="px-2 pt-2">
      <button onClick={onProjectClick} className="flex w-full items-center gap-2 rounded-[4px] border border-transparent px-1.5 py-1 text-left transition-colors duration-150 hover:border-line hover:bg-hover">
        <span className="flex size-6 items-center justify-center rounded-[3px] border border-ink bg-ink font-mono text-[10px] text-page shadow-hairline">ga</span>
        <span className="min-w-0 flex-1"><span className="block truncate text-[12.5px] font-medium leading-4">{project}</span><span className="block truncate text-[10.5px] leading-4 text-ink-3">{deployment}</span></span>
        <svg width="10" height="10" viewBox="0 0 12 12" fill="none" className="text-ink-3" aria-hidden><path d="m3 4.5 3 3 3-3" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" strokeLinejoin="round" /></svg>
      </button>
    </div>
    <nav className="mt-3 px-2" aria-label="Workspace">
      <div className="mb-1 px-1.5 font-mono text-[10px] font-medium uppercase text-ink-3">Workspace</div>
      {items.map((item) => <button key={item.key} type="button" onClick={() => navigate(item.key)} aria-current={active === item.key ? 'page' : undefined} className={`mb-px flex h-7.5 w-full items-center gap-2 rounded-[4px] border px-1.5 text-left transition-[background-color,border-color,color,transform] duration-150 active:scale-[0.985] ${active === item.key ? 'border-line bg-hover-2 text-ink shadow-hairline' : 'border-transparent text-ink-2 hover:border-line hover:bg-hover hover:text-ink'}`}>
        <span className={active === item.key ? 'text-accent-ink' : 'text-ink-3'}><NavIcon kind={item.key} /></span>
        <span className="flex-1 text-[12.5px] font-medium">{item.label}</span>
        {item.shortcut && <span className="text-[10.5px] tabular-nums text-ink-3">{item.key === 'sessions' ? conversationCount : item.shortcut}</span>}
      </button>)}
    </nav>
    <div className="mt-auto border-t border-line p-2">
      <div className="flex items-center gap-2 rounded-[3px] px-1.5 py-1 text-[11.5px] text-ink-3">
        <span className="size-1.5 rounded-full bg-green" /><span className="flex-1 truncate">127.0.0.1:7654</span><span>v0.4.0</span>
      </div>
    </div>
  </aside>
}
