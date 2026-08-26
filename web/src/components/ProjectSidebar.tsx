import { useEffect, useState } from 'react'

export type RegentView = 'sessions' | 'team' | 'files' | 'skills' | 'settings'
export type SettingsView = 'general' | 'status' | 'users' | 'data'

const items: { key: Exclude<RegentView, 'settings'>; label: string }[] = [
  { key: 'sessions', label: 'Sessions' },
  { key: 'team', label: 'Team' },
  { key: 'files', label: 'Browse' },
  { key: 'skills', label: 'Skills' },
]

const settingsItems: { key: SettingsView; label: string }[] = [
  { key: 'general', label: 'General' },
  { key: 'status', label: 'Status' },
  { key: 'users', label: 'Users' },
  { key: 'data', label: 'Data' },
]

function NavIcon({ kind }: { kind: RegentView }) {
  const paths: Record<RegentView, React.ReactNode> = {
    sessions: <><path d="M5 6.5h14M5 11.5h14M5 16.5h9" /><circle cx="3" cy="6.5" r=".5" fill="currentColor" /><circle cx="3" cy="11.5" r=".5" fill="currentColor" /><circle cx="3" cy="16.5" r=".5" fill="currentColor" /></>,
    team: <><circle cx="9" cy="8" r="3" /><path d="M3.5 19a5.5 5.5 0 0 1 11 0" /><circle cx="17" cy="9" r="2.2" /><path d="M15.5 14.5A4.5 4.5 0 0 1 21 19" /></>,
    files: <><path d="M4 6h6l2-2h8v16H4zM4 9h16" /></>,
    skills: <><path d="M12 3.5 14.2 9l5.8.3-4.5 3.7 1.5 5.6L12 15.5 6.9 18.6l1.5-5.6L4 9.3 9.8 9z" /></>,
    settings: <><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06A1.7 1.7 0 0 0 15 19.4a1.7 1.7 0 0 0-1 .6 1.7 1.7 0 0 0-.4 1.1V21h-4v-.1A1.7 1.7 0 0 0 8.6 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.2 15a1.7 1.7 0 0 0-.6-1 1.7 1.7 0 0 0-1.1-.4H2.4v-4h.1A1.7 1.7 0 0 0 4.2 8.6a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 8.6 4.2a1.7 1.7 0 0 0 1-.6A1.7 1.7 0 0 0 10 2.5v-.1h4v.1a1.7 1.7 0 0 0 1 1.7 1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.4 8.6a1.7 1.7 0 0 0 .6 1 1.7 1.7 0 0 0 1.1.4h.1v4h-.1a1.7 1.7 0 0 0-1.7 1Z" /></>,
  }
  return <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden>{paths[kind]}</svg>
}

export interface ProjectSidebarProps {
  project?: string
  active?: RegentView
  settingsSection?: SettingsView
  onNavigate?: (view: RegentView) => void
  onSettingsNavigate?: (section: SettingsView) => void
  collapsed?: boolean
  onCollapsedChange?: (collapsed: boolean) => void
  fill?: boolean
  userName?: string
  userDetail?: string
  onUserClick?: () => void
}

function initialsFor(value: string) {
  const words = value.split(/[^a-zA-Z0-9]+/).filter(Boolean)
  if (!words.length) return '··'
  return words.slice(0, 2).map((word) => word[0]).join('').toUpperCase()
}

/** Resizable workspace navigation. Repository context stays at the top; the bottom is reserved
 * for the current user, while server status lives inside the Settings disclosure. */
export function ProjectSidebar({ project = 're_gent', active: controlled, settingsSection = 'general', onNavigate, onSettingsNavigate, collapsed: controlledCollapsed, onCollapsedChange, fill = false, userName = 'Local user', userDetail = 'Local workspace', onUserClick }: ProjectSidebarProps) {
  const [localActive, setLocalActive] = useState<RegentView>('sessions')
  const active = controlled ?? localActive
  const [localCollapsed, setLocalCollapsed] = useState(false)
  const collapsed = controlledCollapsed ?? localCollapsed
  const [settingsOpen, setSettingsOpen] = useState(active === 'settings')
  useEffect(() => { if (active === 'settings') setSettingsOpen(true) }, [active])

  const navigate = (view: RegentView) => { setLocalActive(view); onNavigate?.(view) }
  const setCollapsed = (next: boolean) => { setLocalCollapsed(next); onCollapsedChange?.(next) }
  const settingsClick = () => {
    if (collapsed) { navigate('settings'); return }
    setSettingsOpen((value) => !value)
  }
  const labelClass = `min-w-0 overflow-hidden whitespace-nowrap transition-[opacity,max-width] duration-150 motion-reduce:transition-none ${collapsed ? 'max-w-0 opacity-0' : 'max-w-[240px] flex-1 opacity-100'}`

  return <aside className={`relative flex h-full min-h-screen flex-col overflow-hidden border-r border-line bg-canvas transition-[width] duration-150 motion-reduce:transition-none ${fill ? 'w-full' : collapsed ? 'w-16' : 'w-52'}`} aria-label="Project navigation">
    <div className={`flex h-12 shrink-0 items-center gap-2 border-b border-line ${collapsed ? 'justify-center px-0' : 'px-3'}`}>
      <div className="group/logo relative flex size-7 shrink-0 items-center justify-center">
        <a href="https://www.re-gent.dev/" aria-label="Visit re_gent" className="relative z-10 flex size-7 items-center justify-center rounded-[4px] transition-opacity duration-150 group-hover/logo:opacity-0 motion-reduce:transition-none"><img src="/favicon.svg" alt="" className="block size-7" /></a>
        <button type="button" onClick={() => setCollapsed(!collapsed)} aria-expanded={!collapsed} aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'} className="absolute inset-0 z-0 flex size-7 items-center justify-center rounded-[4px] text-ink-3 opacity-0 transition-[opacity,background-color,color] duration-150 hover:bg-hover hover:text-ink group-hover/logo:z-20 group-hover/logo:opacity-100 focus:z-20 focus:opacity-100 motion-reduce:transition-none">
          <svg width="11" height="11" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden><path d={collapsed ? 'm4.5 2.5 4 3.5-4 3.5' : 'M7.5 2.5 3.5 6l4 3.5'} /></svg>
        </button>
      </div>
      <div className={labelClass} aria-hidden={collapsed}><span className="block truncate text-[12px] font-semibold leading-4">{project}</span><span className="block text-[9.5px] uppercase tracking-[0.06em] text-ink-3">Workspace</span></div>
    </div>

    <nav className="mt-1 flex-1 overflow-y-auto px-2" aria-label="Workspace">
      {items.map((item) => {
        const selected = active === item.key
        return <button key={item.key} type="button" onClick={() => navigate(item.key)} aria-current={selected ? 'page' : undefined} aria-label={collapsed ? item.label : undefined} title={item.label} className={`flex h-7 w-full items-center rounded-[4px] border px-1.5 text-left text-[12px] leading-none ${collapsed ? 'justify-center gap-0' : 'gap-1.5'} ${selected ? 'border-line bg-hover-2 text-ink shadow-hairline' : 'border-transparent text-ink-2 hover:border-line hover:bg-hover hover:text-ink'}`}>
          <span className={`flex w-5 shrink-0 justify-center ${selected ? 'text-accent-ink' : 'text-ink-3'}`}><NavIcon kind={item.key} /></span><span className={labelClass} aria-hidden={collapsed}>{item.label}</span>
        </button>
      })}

      <div className="mt-0.5 border-t border-line pt-0.5">
        <button type="button" onClick={settingsClick} aria-current={active === 'settings' ? 'page' : undefined} aria-expanded={collapsed ? undefined : settingsOpen} aria-label={collapsed ? 'Settings' : undefined} title="Settings" className={`flex h-7 w-full items-center rounded-[4px] border px-1.5 text-left text-[12px] leading-none ${collapsed ? 'justify-center gap-0' : 'gap-1.5'} ${active === 'settings' ? 'border-line bg-hover-2 text-ink shadow-hairline' : 'border-transparent text-ink-2 hover:border-line hover:bg-hover hover:text-ink'}`}>
          <span className={`flex w-5 shrink-0 justify-center ${active === 'settings' ? 'text-accent-ink' : 'text-ink-3'}`}><NavIcon kind="settings" /></span><span className={labelClass} aria-hidden={collapsed}>Settings</span>{!collapsed && <svg width="10" height="10" viewBox="0 0 12 12" fill="none" className={`shrink-0 text-ink-3 transition-transform ${settingsOpen ? '' : '-rotate-90'}`} aria-hidden><path d="m3 4.5 3 3 3-3" stroke="currentColor" strokeWidth="1.25" /></svg>}
        </button>
        {!collapsed && settingsOpen && <div className="mb-0.5 ml-7 mt-px border-l border-line pl-1.5">{settingsItems.map((item) => <button key={item.key} type="button" onClick={() => onSettingsNavigate?.(item.key)} aria-current={active === 'settings' && settingsSection === item.key ? 'page' : undefined} className={`flex h-6 w-full items-center rounded-[3px] px-2 text-left text-[11px] ${active === 'settings' && settingsSection === item.key ? 'bg-hover-2 font-medium text-ink' : 'text-ink-3 hover:bg-hover hover:text-ink-2'}`}>{item.label}</button>)}</div>}
      </div>
    </nav>

    <div className="mt-auto border-t border-line p-2">
      <button type="button" onClick={onUserClick} aria-label={collapsed ? `User: ${userName}` : undefined} title={userName} className={`flex w-full items-center rounded-[5px] border border-transparent py-1.5 text-left hover:border-line hover:bg-hover ${collapsed ? 'justify-center px-0' : 'gap-2 px-1.5'}`}>
        <span className="flex size-7 shrink-0 items-center justify-center rounded-full bg-ink font-mono text-[9.5px] font-semibold text-page">{initialsFor(userName)}</span>
        <span className={labelClass} aria-hidden={collapsed}><span className="block truncate text-[11.5px] font-medium leading-4">{userName}</span><span className="block truncate text-[9.5px] leading-3.5 text-ink-3">{userDetail}</span></span>
        {!collapsed && <svg width="10" height="10" viewBox="0 0 12 12" fill="none" className="shrink-0 text-ink-3" aria-hidden><path d="m3 4.5 3 3 3-3" stroke="currentColor" strokeWidth="1.25" /></svg>}
      </button>
    </div>
  </aside>
}
