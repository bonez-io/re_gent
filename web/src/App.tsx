import { useMemo, useState } from 'react'
import { ConversationTranscript } from './components/ConversationTranscript'
import { FilesBlame } from './components/FilesBlame'
import { ProjectSidebar, type RegentView } from './components/ProjectSidebar'
import { SessionRow } from './components/SessionRow'
import { SyncPanel } from './components/SyncPanel'
import { blameLines, conversations, transcript } from './mocks/regent'

function Topbar({ view }: { view: RegentView }) {
  const labels: Record<RegentView, string> = { conversations: 'Conversations', transcript: 'Full transcript', steps: 'Steps', files: 'Files & blame', sync: 'Sync status' }
  return <header className="flex h-10 shrink-0 items-center gap-2 border-b border-line bg-canvas px-3">
    <span className="text-[10.5px] text-ink-3">girlfriend-assistant</span><span className="text-ink-3/50">/</span><span className="text-[11px] font-medium">{labels[view]}</span>
    <label className="ml-auto hidden h-6 w-52 items-center gap-1.5 rounded-[7px] bg-field px-2 shadow-hairline transition-[background-color,box-shadow] duration-150 focus-within:bg-hover-2 focus-within:shadow-btn sm:flex"><span className="text-[10px] text-ink-3">⌕</span><input className="min-w-0 flex-1 bg-transparent text-[10px] outline-none placeholder:text-ink-3" placeholder="Search prompts, steps, files" /><kbd className="text-[9px] text-ink-3">/</kbd></label>
    <span className="ml-1 flex items-center gap-1.5 text-[9.5px] text-ink-3"><span className="size-1.5 rounded-full bg-green" />synced</span>
  </header>
}

function FilterControl({ children }: { children: React.ReactNode }) {
  return <button type="button" className="inline-flex h-6 items-center gap-1.5 rounded-[7px] bg-field px-2 text-[9.5px] font-medium leading-none text-ink-2 shadow-hairline transition-[background-color,transform] duration-150 hover:bg-hover-2 active:scale-[0.97] max-sm:hidden">{children}<svg width="9" height="9" viewBox="0 0 12 12" fill="none" className="mt-px text-ink-3 transition-transform duration-150" aria-hidden><path d="m3 4.5 3 3 3-3" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" strokeLinejoin="round" /></svg></button>
}

function FilterBar({ query, setQuery }: { query: string; setQuery: (query: string) => void }) {
  return <div className="flex h-9 items-center gap-1.5 border-b border-line px-3">
    <input value={query} onChange={(event) => setQuery(event.target.value)} className="h-6 w-60 rounded-[7px] bg-field px-2 text-[10px] outline-none shadow-hairline transition-[background-color,box-shadow] duration-150 placeholder:text-ink-3 focus:bg-hover-2 focus:shadow-btn max-sm:min-w-0 max-sm:flex-1" placeholder="Filter conversations…" />
    {['All branches', 'All agents', 'Any status'].map((label) => <FilterControl key={label}>{label}</FilterControl>)}
    <span className="ml-auto text-[9.5px] tabular-nums text-ink-3 max-md:hidden">7 conversations · 155 steps</span>
  </div>
}

function ConversationIndex({ onOpen }: { onOpen: () => void }) {
  const [query, setQuery] = useState('')
  const visible = useMemo(() => conversations.filter((item) => item.title.toLowerCase().includes(query.toLowerCase()) || item.branch.toLowerCase().includes(query.toLowerCase())), [query])
  return <div className="min-h-0 flex-1 overflow-auto bg-canvas">
    <div className="border-b border-line px-4 py-3"><h1 className="m-0 text-[14px] font-semibold tracking-[-0.01em]">Conversations</h1><p className="m-0 mt-0.5 text-[10px] text-ink-3">Captured agent work across sessions, branches, and hosts.</p></div>
    <FilterBar query={query} setQuery={setQuery} />
    <div className="mx-auto max-w-[980px] px-3 pb-8 pt-2">
      {(['Today', 'Yesterday', 'Earlier'] as const).map((group) => { const rows = visible.filter((item) => item.dateGroup === group); return rows.length > 0 && <section key={group} className="mb-2.5"><div className="flex h-6 items-center px-2 text-[10px] font-medium text-ink-3">{group}<span className="ml-1.5 tabular-nums text-ink-3/80">{rows.length}</span></div><div className="overflow-hidden rounded-[8px] border border-line">{rows.map((item) => <SessionRow key={item.id} {...item} onClick={onOpen} />)}</div></section> })}
    </div>
  </div>
}

function ConversationList({ onOpen }: { onOpen: () => void }) {
  return <aside className="min-h-0 overflow-auto border-r border-line bg-canvas max-lg:hidden" aria-label="Conversation list"><div className="sticky top-0 z-10 flex h-9 items-center border-b border-line bg-canvas px-2.5"><span className="text-[10.5px] font-semibold">Conversations</span><span className="ml-auto"><FilterControl>Filter</FilterControl></span></div>{conversations.map((item, index) => <SessionRow key={item.id} {...item} selected={index === 0} onClick={onOpen} />)}</aside>
}

function TranscriptScreen({ onOpen }: { onOpen: () => void }) {
  return <div className="grid min-h-0 flex-1 grid-cols-[300px_minmax(0,1fr)] max-lg:grid-cols-1">
    <ConversationList onOpen={onOpen} />
    <section className="min-h-0 overflow-auto bg-canvas">
      <div className="sticky top-0 z-10 border-b border-line bg-canvas/95 px-3 py-2 backdrop-blur"><div className="flex items-center gap-2"><h1 className="m-0 truncate text-[12.5px] font-semibold">Stabilize reminder scheduling</h1><span className="rounded-[3px] bg-green-tint px-1.5 py-0.5 text-[8.5px] font-medium text-green">capturing</span><button className="ml-auto text-[14px] text-ink-3">•••</button></div><div className="mt-1 flex flex-wrap gap-x-2 text-[9px] text-ink-3"><span className="font-mono text-accent-ink">codex:01JZQ8MX7D</span><span>main</span><span>Codex · gpt-5.6</span><span>42 steps</span><span>7 files</span></div></div>
      <ConversationTranscript entries={transcript} />
    </section>
  </div>
}

function StepsScreen({ onOpen }: { onOpen: () => void }) {
  const steps = transcript.filter((entry) => entry.type === 'step')
  return <section className="min-h-0 flex-1 overflow-auto bg-canvas p-4">
    <div className="mb-4"><h1 className="m-0 text-[14px] font-semibold">Steps</h1><p className="m-0 mt-1 text-[10px] text-ink-3">Immutable checkpoints ordered by session ref.</p></div>
    <div className="border border-line"><div className="grid h-7 grid-cols-[90px_90px_minmax(160px,1fr)_90px_80px_65px_60px] items-center bg-inset px-2 text-[9px] font-medium text-ink-3"><span>Step</span><span>Parent</span><span>Session</span><span>Turn</span><span>Tree</span><span>Tokens</span><span>Files</span></div>{steps.map((step, index) => <button key={step.id} onClick={onOpen} className="grid h-8 w-full grid-cols-[90px_90px_minmax(160px,1fr)_90px_80px_65px_60px] items-center border-t border-line px-2 text-left font-mono text-[9.5px] text-ink-3 hover:bg-hover"><span className="text-accent-ink">{step.hash}</span><span>{index ? steps[index - 1].hash : '41ac200'}</span><span className="truncate">codex:01JZQ8MX7D</span><span>{step.turn}</span><span>{step.tree}</span><span className="tabular-nums">{step.tokens}</span><span>{step.files}</span></button>)}</div>
    <div className="mt-5 grid grid-cols-3 border border-line max-md:grid-cols-1">{[['155', 'Total steps'], ['42,819', 'Captured tokens'], ['1,284', 'Stored objects']].map(([value, label]) => <div key={label} className="border-r border-line px-3 py-3 last:border-r-0 max-md:border-b max-md:border-r-0"><div className="font-mono text-[14px] text-ink">{value}</div><div className="mt-1 text-[9.5px] text-ink-3">{label}</div></div>)}</div>
  </section>
}

function App() {
  const [view, setView] = useState<RegentView>('conversations')
  const openTranscript = () => setView('transcript')
  return <div className="flex h-screen min-h-[560px] overflow-hidden bg-page text-ink">
    <div className="shrink-0 max-sm:hidden"><ProjectSidebar active={view} onNavigate={setView} /></div>
    <main className="flex min-w-0 flex-1 flex-col"><Topbar view={view} />
      <div key={view} className="regent-view flex min-h-0 flex-1">
        {view === 'conversations' && <ConversationIndex onOpen={openTranscript} />}
        {view === 'transcript' && <TranscriptScreen onOpen={openTranscript} />}
        {view === 'steps' && <StepsScreen onOpen={openTranscript} />}
        {view === 'files' && <FilesBlame lines={blameLines} />}
        {view === 'sync' && <div className="min-h-0 flex-1 overflow-auto bg-canvas"><SyncPanel /></div>}
      </div>
      <nav className="hidden h-10 shrink-0 items-center justify-around border-t border-line bg-canvas max-sm:flex">{(['conversations', 'transcript', 'steps', 'files', 'sync'] as RegentView[]).map((item) => <button key={item} onClick={() => setView(item)} className={`px-2 text-[9px] ${view === item ? 'text-accent-ink' : 'text-ink-3'}`}>{item === 'conversations' ? 'Chats' : item}</button>)}</nav>
    </main>
  </div>
}

export default App
