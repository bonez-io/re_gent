import { useLayoutEffect, useRef, useState } from 'react'

type Section = 'Explore' | 'Server'
type NavItem = { key: string; label: string; section: Section; count?: number }

const items: NavItem[] = [
  { key: 'sessions', label: 'Sessions', section: 'Explore', count: 12 },
  { key: 'step', label: 'Step context', section: 'Explore' },
  { key: 'files', label: 'Files & blame', section: 'Explore' },
  { key: 'connection', label: 'localhost:7654', section: 'Server' },
]

function NavIcon({ kind }: { kind: string }) {
  const paths: Record<string, React.ReactNode> = {
    sessions: <><rect x="4" y="4" width="16" height="16" rx="4.5" /><path d="M8 9h8M8 12h8M8 15h5" /></>,
    step: <><path d="M6 4v16M6 8h5l2-2h5v7h-5l-2-2H6" /></>,
    files: <><path d="M4 7.5h6l2-2h8v14H4z" /><path d="M4 10h16" /></>,
    connection: <><circle cx="12" cy="12" r="3" /><path d="M5.6 8.4a7.5 7.5 0 0 1 12.8 0M8.3 10.1a4.4 4.4 0 0 1 7.4 0" /></>,
  }
  return <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">{paths[kind]}</svg>
}

export interface ProjectSidebarProps { project?: string; deployment?: string; initialActive?: string }

/** Adapted from Beautiful UI's MIT-licensed Sidebar Nav primitive. */
export function ProjectSidebar({ project = 'girlfriend-assistant', deployment = 'Self-hosted', initialActive = 'sessions' }: ProjectSidebarProps) {
  const [active, setActive] = useState(initialActive)
  const [hovered, setHovered] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [box, setBox] = useState<{ top: number; height: number } | null>(null)
  const navRef = useRef<HTMLDivElement>(null)
  const itemRefs = useRef<Record<string, HTMLButtonElement | null>>({})

  useLayoutEffect(() => {
    const container = navRef.current
    const target = itemRefs.current[hovered ?? active]
    if (!container || !target) return
    const containerRect = container.getBoundingClientRect()
    const targetRect = target.getBoundingClientRect()
    setBox({ top: targetRect.top - containerRect.top, height: targetRect.height })
  }, [hovered, active])

  return <aside className="w-60 rounded-card bg-surface p-2 shadow-raised" aria-label="Project navigation">
    <button type="button" className="mb-2 flex w-full items-center gap-2.5 rounded-control p-1.5 text-left transition-[background-color,transform] duration-100 hover:bg-hover active:scale-[0.96]">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-[9px] bg-accent text-[13px] font-semibold text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.28)]">r</span>
      <span className="min-w-0 flex-1"><span className="block truncate text-[13px] font-medium leading-tight text-ink">{project}</span><span className="block truncate text-[11px] leading-tight text-ink-3">{deployment}</span></span>
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="var(--ink-3)" strokeWidth="1.9"><path d="M6 9l6 6 6-6" /></svg>
    </button>
    <label className="mb-2 flex h-8 items-center gap-2 rounded-control bg-inset px-2.5 shadow-hairline">
      <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="var(--ink-3)" strokeWidth="2"><circle cx="11" cy="11" r="7" /><path d="M21 21l-4.3-4.3" /></svg>
      <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Quick search" aria-label="Quick search" className="min-w-0 flex-1 bg-transparent text-[12.5px] text-ink outline-none placeholder:text-ink-3" />
      <kbd className="flex size-4.5 items-center justify-center rounded-[5px] bg-surface text-[10px] text-ink-3 shadow-hairline">/</kbd>
    </label>
    <div ref={navRef} onMouseLeave={() => setHovered(null)} className="relative flex flex-col gap-2">
      <span aria-hidden className="pointer-events-none absolute inset-x-0 rounded-[7px] bg-hover" style={{ top: box?.top ?? 0, height: box?.height ?? 0, opacity: box ? 1 : 0, transition: 'top 220ms cubic-bezier(0.23,1,0.32,1), height 220ms cubic-bezier(0.23,1,0.32,1), opacity 150ms ease' }} />
      {(['Explore', 'Server'] as Section[]).map((section) => <div key={section}>
        <div className="px-2 pb-1 pt-1 text-[10.5px] font-medium uppercase tracking-[0.08em] text-ink-3">{section}</div>
        <div className="flex flex-col gap-px">{items.filter((item) => item.section === section && item.label.toLowerCase().includes(query.toLowerCase())).map((item) => {
          const selected = active === item.key
          return <button key={item.key} ref={(node) => { itemRefs.current[item.key] = node }} type="button" onMouseEnter={() => setHovered(item.key)} onFocus={() => setHovered(item.key)} onBlur={() => setHovered(null)} onClick={() => setActive(item.key)} aria-current={selected ? 'page' : undefined} className="relative z-10 flex w-full items-center gap-2 rounded-[7px] px-2 py-1.5 text-left transition-[color,transform] duration-150 active:scale-[0.96]">
            <span className={selected ? 'text-ink' : 'text-ink-3'}><NavIcon kind={item.key} /></span>
            <span className={`min-w-0 flex-1 truncate text-[13px] ${selected ? 'font-semibold text-ink' : 'font-medium text-ink-2'}`}>{item.label}</span>
            {item.count !== undefined && <span className="flex h-4.5 min-w-4.5 items-center justify-center rounded-full bg-surface px-1 text-[10.5px] font-semibold text-ink-2 shadow-hairline">{item.count}</span>}
            {item.key === 'connection' && <span className="size-1.5 rounded-full bg-green" aria-label="Connected" />}
          </button>
        })}</div>
      </div>)}
    </div>
  </aside>
}
