import { useMemo, useState } from 'react'
import { sessionToConversation } from '../api/adapters'
import type { SessionSummary } from '../api/types'
import { SessionRow } from './SessionRow'

type SearchMode = 'keyword' | 'semantic'
type DateRange = 'any' | 'today' | '7d' | '30d' | 'custom'
type SortOrder = 'recent' | 'oldest' | 'steps'

const memberFor = (session: SessionSummary) => session.author?.name || session.author?.email || 'Unknown user'
const normalizedAgent = (session: SessionSummary) => session.agent_id || session.session_id.split(':')[0] || 'unknown'

function startOfToday() {
  const date = new Date()
  date.setHours(0, 0, 0, 0)
  return date.valueOf()
}

function dateMatches(value: string, range: DateRange, after: string, before: string) {
  if (range === 'any') return true
  const timestamp = new Date(value).valueOf()
  if (!Number.isFinite(timestamp)) return false
  if (range === 'today') return timestamp >= startOfToday()
  if (range === '7d') return timestamp >= Date.now() - 7 * 86_400_000
  if (range === '30d') return timestamp >= Date.now() - 30 * 86_400_000
  const lower = after ? new Date(`${after}T00:00:00`).valueOf() : Number.NEGATIVE_INFINITY
  const upper = before ? new Date(`${before}T23:59:59.999`).valueOf() : Number.POSITIVE_INFINITY
  return timestamp >= lower && timestamp <= upper
}

export interface SessionSearchProps {
  sessions: SessionSummary[]
  selectedId?: string
  onSelect: (sessionId: string) => void
}

/** Search-and-filter workspace for captured sessions. Semantic mode is prepared for a server
 * index; until it is connected, the UI states that it is falling back to metadata tokens. */
export function SessionSearch({ sessions, selectedId, onSelect }: SessionSearchProps) {
  const [query, setQuery] = useState('')
  const [mode, setMode] = useState<SearchMode>('keyword')
  const [filtersOpen, setFiltersOpen] = useState(false)
  const [user, setUser] = useState('all')
  const [agent, setAgent] = useState('all')
  const [dateRange, setDateRange] = useState<DateRange>('any')
  const [after, setAfter] = useState('')
  const [before, setBefore] = useState('')
  const [sort, setSort] = useState<SortOrder>('recent')

  const users = useMemo(() => [...new Set(sessions.map(memberFor))].sort(), [sessions])
  const agents = useMemo(() => [...new Set(sessions.map(normalizedAgent))].sort(), [sessions])
  const activeFilters = Number(user !== 'all') + Number(agent !== 'all') + Number(dateRange !== 'any')

  const rows = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase()
    const terms = normalizedQuery.split(/\s+/).filter(Boolean)
    const filtered = sessions.filter((session) => {
      const haystack = [session.title, session.session_id, memberFor(session), normalizedAgent(session)].join(' ').toLowerCase()
      const searchMatch = !normalizedQuery || (mode === 'keyword' ? haystack.includes(normalizedQuery) : terms.every((term) => haystack.includes(term)))
      return searchMatch
        && (user === 'all' || memberFor(session) === user)
        && (agent === 'all' || normalizedAgent(session) === agent)
        && dateMatches(session.last_activity, dateRange, after, before)
    })
    filtered.sort((a, b) => sort === 'steps'
      ? b.step_count - a.step_count
      : sort === 'oldest'
        ? new Date(a.last_activity).valueOf() - new Date(b.last_activity).valueOf()
        : new Date(b.last_activity).valueOf() - new Date(a.last_activity).valueOf())
    return filtered.map(sessionToConversation)
  }, [sessions, query, mode, user, agent, dateRange, after, before, sort])

  const reset = () => { setUser('all'); setAgent('all'); setDateRange('any'); setAfter(''); setBefore(''); setSort('recent') }

  return <aside className="flex h-full min-h-0 flex-col bg-canvas" aria-label="Session search and results">
    <div className="shrink-0 border-b border-line px-2.5 pb-2 pt-2.5">
      <div className="mb-2 flex items-center justify-between gap-2">
        <div><h1 className="m-0 text-[13px] font-semibold leading-4">Sessions</h1><p className="m-0 text-[10px] leading-4 text-ink-3">{rows.length} of {sessions.length} captured</p></div>
        <button type="button" aria-expanded={filtersOpen} onClick={() => setFiltersOpen((value) => !value)} className={`flex h-7 items-center gap-1.5 rounded-[4px] border px-2 text-[10.5px] shadow-hairline ${filtersOpen || activeFilters ? 'border-accent/40 bg-accent-tint text-accent-ink' : 'border-line bg-field text-ink-2 hover:bg-hover'}`}>
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden><path d="M4 6h16M7 12h10M10 18h4" /></svg>
          Filters{activeFilters > 0 && <span className="rounded-full bg-accent px-1 text-[9px] text-white">{activeFilters}</span>}
        </button>
      </div>

      <div className="flex overflow-hidden rounded-[5px] bg-field shadow-hairline focus-within:shadow-btn">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" className="ml-2.5 mt-2 shrink-0 text-ink-3" aria-hidden><circle cx="10.5" cy="10.5" r="6" /><path d="m15 15 5 5" /></svg>
        <input value={query} onChange={(event) => setQuery(event.target.value)} className="h-8 min-w-0 flex-1 bg-transparent px-2 text-[11.5px] outline-none" placeholder={mode === 'semantic' ? 'Describe the work you remember…' : 'Search title, user, agent, or ID…'} aria-label="Search sessions" />
        {query && <button type="button" onClick={() => setQuery('')} aria-label="Clear session search" className="px-2 text-ink-3 hover:text-ink">×</button>}
      </div>

      <div className="mt-1.5 flex items-center gap-1">
        {(['keyword', 'semantic'] as SearchMode[]).map((item) => <button key={item} type="button" aria-pressed={mode === item} onClick={() => setMode(item)} className={`rounded-[3px] px-1.5 py-0.5 text-[9.5px] capitalize ${mode === item ? 'bg-hover-2 font-medium text-ink' : 'text-ink-3 hover:text-ink-2'}`}>{item === 'semantic' ? '✦ Semantic' : 'Keyword'}</button>)}
        {mode === 'semantic' && <span className="ml-auto text-[9px] text-amber-500" title="Server semantic index is not connected">metadata fallback</span>}
      </div>

      {filtersOpen && <div className="mt-2 grid gap-2 rounded-[5px] border border-line bg-inset p-2">
        <label className="grid gap-1 text-[9.5px] uppercase tracking-[0.05em] text-ink-3">User<select value={user} onChange={(event) => setUser(event.target.value)} className="h-7 min-w-0 rounded-[4px] border border-line bg-canvas px-1.5 text-[10.5px] normal-case tracking-normal text-ink outline-none"><option value="all">All users</option>{users.map((item) => <option key={item}>{item}</option>)}</select></label>
        <label className="grid gap-1 text-[9.5px] uppercase tracking-[0.05em] text-ink-3">Coding agent<select value={agent} onChange={(event) => setAgent(event.target.value)} className="h-7 min-w-0 rounded-[4px] border border-line bg-canvas px-1.5 text-[10.5px] normal-case tracking-normal text-ink outline-none"><option value="all">All agents</option>{agents.map((item) => <option key={item}>{item}</option>)}</select></label>
        <label className="grid gap-1 text-[9.5px] uppercase tracking-[0.05em] text-ink-3">Date<select value={dateRange} onChange={(event) => setDateRange(event.target.value as DateRange)} className="h-7 min-w-0 rounded-[4px] border border-line bg-canvas px-1.5 text-[10.5px] normal-case tracking-normal text-ink outline-none"><option value="any">Any time</option><option value="today">Today</option><option value="7d">Last 7 days</option><option value="30d">Last 30 days</option><option value="custom">Custom range</option></select></label>
        {dateRange === 'custom' && <div className="grid grid-cols-2 gap-1.5"><label className="grid gap-1 text-[9px] text-ink-3">After<input type="date" value={after} onChange={(event) => setAfter(event.target.value)} className="h-7 min-w-0 rounded-[4px] border border-line bg-canvas px-1 text-[9.5px] text-ink" /></label><label className="grid gap-1 text-[9px] text-ink-3">Before<input type="date" value={before} onChange={(event) => setBefore(event.target.value)} className="h-7 min-w-0 rounded-[4px] border border-line bg-canvas px-1 text-[9.5px] text-ink" /></label></div>}
        <div className="flex items-end gap-1.5"><label className="grid min-w-0 flex-1 gap-1 text-[9.5px] uppercase tracking-[0.05em] text-ink-3">Sort<select value={sort} onChange={(event) => setSort(event.target.value as SortOrder)} className="h-7 min-w-0 rounded-[4px] border border-line bg-canvas px-1.5 text-[10.5px] normal-case tracking-normal text-ink outline-none"><option value="recent">Most recent</option><option value="oldest">Oldest first</option><option value="steps">Most steps</option></select></label><button type="button" onClick={reset} className="h-7 rounded-[4px] px-2 text-[10px] text-ink-3 hover:bg-hover hover:text-ink">Reset</button></div>
      </div>}
    </div>

    <div className="min-h-0 flex-1 overflow-y-auto">
      {rows.length ? rows.map((row) => <SessionRow key={row.id} {...row} selected={row.id === selectedId} onClick={() => onSelect(row.id)} />) : <div className="px-3 py-8 text-center text-[11.5px] text-ink-3"><p className="m-0 font-medium text-ink-2">No matching sessions</p><p className="m-0 mt-1 text-[10.5px]">Try fewer filters or a broader query.</p></div>}
    </div>
  </aside>
}
