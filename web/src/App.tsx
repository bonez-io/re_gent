import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom'
import { ApiError, OfflineError, api } from './api/client'
import { logToTranscript, transcriptToEntries } from './api/adapters'
import { languageForPath } from './lib/highlight'
import type { StatusResponse } from './api/types'
import { AgentIcon, agentColor, agentLabel } from './components/AgentIcon'
import { BlameView } from './components/BlameView'
import { ConversationTranscript } from './components/ConversationTranscript'
import { FileTree } from './components/FileTree'
import { ProjectSidebar, type RegentView, type SettingsView } from './components/ProjectSidebar'
import { ResizeHandle } from './components/ResizeHandle'
import { SessionSearch } from './components/SessionSearch'
import { TeamDashboard } from './components/TeamDashboard'
import { usePersistentPanelSize } from './lib/panelSize'
import { SettingsScreen, type SettingsSection } from './screens/SettingsScreen'
import { SkillsScreen } from './screens/SkillsScreen'

const defaultRepo = import.meta.env.VITE_REGENT_REPO_ID as string | undefined
const connectServerUrl = ((import.meta.env.VITE_REGENT_SERVER_URL as string | undefined) || (import.meta.env.PROD ? window.location.origin : 'http://127.0.0.1:7654')).replace(/\/+$/, '')
const connectCommand = `rgt connect ${connectServerUrl}`
const apiVersionOf = (data: StatusResponse) => typeof data.service === 'string' || !data.service.api_version ? undefined : `API v${data.service.api_version}`
// A stopped server rarely fails the fetch: the dev proxy and any production
// reverse proxy answer for it with a gateway status instead. Treating only a
// failed fetch as offline let the chrome report "connected" with nothing behind
// it, so gateway statuses count as unreachable too.
const isUnreachable = (error: unknown) => error instanceof OfflineError || (error instanceof ApiError && [502, 503, 504].includes(error.status))
const short = (value?: string) => value ? value.slice(0, 8) : '—'
const viewFor = (path: string): RegentView => path.includes('/settings') ? 'settings' : path.endsWith('/team') ? 'team' : path.endsWith('/files') ? 'files' : path.endsWith('/skills') ? 'skills' : 'sessions'
const settingsFor = (path: string): SettingsView => (path.match(/\/settings\/(general|status|users|data)/)?.[1] as SettingsView | undefined) ?? 'general'
const pathFor = (repoId: string, view: RegentView) => `/repos/${encodeURIComponent(repoId)}/${view === 'settings' ? 'settings/general' : view}`

function Pending({ label = 'Loading captured work…' }: { label?: string }) {
  return <div className="flex flex-1 items-center justify-center text-[12px] text-ink-3"><span className="mr-2 size-2 animate-pulse rounded-full bg-accent" />{label}</div>
}

function Problem({ error, onRetry }: { error: Error; onRetry?: () => void }) {
  const offline = isUnreachable(error)
  const missing = error instanceof ApiError && error.status === 404
  return <div className="m-auto max-w-sm px-6 py-10 text-center"><div className={`mx-auto mb-2 size-2 rounded-full ${offline ? 'bg-red' : 'bg-accent'}`} /><h2 className="m-0 text-[15px] font-semibold">{offline ? 'Server disconnected' : missing ? 'Data is not available yet' : 'Could not load this view'}</h2><p className="mt-1 text-[12px] leading-5 text-ink-3">{offline ? 'Start the local re_gent server on 127.0.0.1:7654, then retry.' : error.message}</p>{onRetry && <button onClick={onRetry} className="mt-3 h-8 rounded-[4px] bg-field px-3 text-[12px] shadow-hairline hover:bg-hover-2">Retry</button>}</div>
}

function Empty({ title, detail }: { title: string; detail: string }) {
  return <div className="m-auto max-w-md px-6 py-12 text-center"><img src="/favicon.svg" alt="" className="mx-auto mb-2 size-8 opacity-70" /><h2 className="m-0 text-[15px] font-semibold">{title}</h2><p className="mt-1 text-[12px] leading-5 text-ink-3">{detail}</p></div>
}

function RepoHome() {
  const navigate = useNavigate()
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const repos = useQuery({ queryKey: ['repos'], queryFn: api.listRepos, retry: false, refetchInterval: 1_500 })
  const copyCommand = async () => {
    try {
      await navigator.clipboard.writeText(connectCommand)
      setCopyState('copied')
    } catch {
      setCopyState('failed')
    }
  }
  if (repos.isPending) return <main className="flex min-h-screen bg-page text-ink"><Pending label="Connecting to re_gent…" /></main>
  if (repos.error) return <main className="flex min-h-screen bg-page text-ink"><Problem error={repos.error} onRetry={() => repos.refetch()} /></main>
  if (defaultRepo && repos.data.repos.includes(defaultRepo)) return <Navigate replace to={`/repos/${defaultRepo}/sessions`} />
  if (repos.data.repos.length === 1) return <Navigate replace to={`/repos/${repos.data.repos[0]}/sessions`} />
  const hasRepos = repos.data.repos.length > 0
  return <main className="flex min-h-screen items-center justify-center bg-page p-4 text-ink">
    <section className="w-full max-w-md overflow-hidden rounded-[8px] border border-line bg-canvas shadow-raised">
      <header className="flex items-center gap-2 border-b border-line px-4 py-2.5">
        <img src="/favicon.svg" alt="" className="size-7" />
        <div>
          <h1 className="m-0 text-[15px] font-semibold leading-5">{hasRepos ? 'Open a re_gent repository' : 'Connect a project'}</h1>
          <p className="m-0 text-[11px] leading-4 text-ink-3">{hasRepos ? 'Repositories registered on this server' : 'Run one command from your project directory'}</p>
        </div>
      </header>
      {hasRepos && <div className="p-2">{repos.data.repos.map((repo) => <button key={repo} onClick={() => navigate(`/repos/${repo}/sessions`)} className="flex h-10 w-full items-center rounded-[4px] px-2.5 text-left text-[12.5px] hover:bg-hover"><span className="size-1.5 rounded-full bg-green" /><span className="ml-2 flex-1 font-medium">{repo}</span><span className="text-ink-3">Open →</span></button>)}</div>}
      <div className={`${hasRepos ? 'border-t border-line' : ''} p-4`}>
        {hasRepos && <h2 className="m-0 text-[12px] font-medium">Connect another project</h2>}
        <p className={`${hasRepos ? 'mt-1' : 'mt-0'} mb-2.5 text-[11.5px] leading-5 text-ink-3`}>Open a terminal in the project you want re_gent to track, then run:</p>
        <div className="flex items-center overflow-hidden rounded-[8px] bg-inset shadow-hairline">
          <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap px-3 py-2.5 font-mono text-[11.5px] text-ink-2">{connectCommand}</code>
          <button type="button" onClick={() => void copyCommand()} aria-label="Copy connect command" className="mr-1.5 flex h-7 shrink-0 items-center gap-1.5 rounded-[4px] bg-field px-2 text-[10.5px] font-medium text-ink-2 shadow-hairline hover:bg-hover-2 hover:text-ink">
            {copyState === 'copied' ? <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden><path d="m2.5 6.2 2.1 2.1 4.9-5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" /></svg> : <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden><rect x="4" y="2" width="5.5" height="6" rx="1" stroke="currentColor" /><path d="M7.5 9v.25c0 .69-.56 1.25-1.25 1.25h-3.5c-.69 0-1.25-.56-1.25-1.25v-4C1.5 4.56 2.06 4 2.75 4H3" stroke="currentColor" /></svg>}
            {copyState === 'copied' ? 'Copied' : copyState === 'failed' ? 'Copy failed' : 'Copy'}
          </button>
        </div>
        <div role="status" aria-live="polite" className="mt-3 flex items-center gap-2 rounded-[4px] bg-accent-tint px-2.5 py-2 text-[11px] text-accent-ink">
          <span className="relative flex size-2" aria-hidden><span className="absolute inline-flex size-full animate-ping rounded-full bg-accent opacity-50" /><span className="relative inline-flex size-2 rounded-full bg-accent" /></span>
          <span>Listening for a connected project…</span>
        </div>
        <p className="mb-0 mt-2 text-[10.5px] leading-4 text-ink-3">Keep this page open. re_gent will continue automatically when the project appears.</p>
      </div>
    </section>
  </main>
}

function Shell() {
  const { repoId = '' } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  const active = viewFor(location.pathname)
  const settingsSection = settingsFor(location.pathname)
  const [sidebarWidth, setSidebarWidth] = usePersistentPanelSize('workspace-sidebar', 216, 64, 360)
  const [expandedSidebarWidth, setExpandedSidebarWidth] = useState(Math.max(216, sidebarWidth))
  const sidebarCollapsed = sidebarWidth <= 72
  const resizeSidebar = (next: number) => {
    if (next < 96) setSidebarWidth(64)
    else { setSidebarWidth(next); setExpandedSidebarWidth(next) }
  }
  const setSidebarCollapsed = (collapsed: boolean) => {
    if (collapsed) { setExpandedSidebarWidth(Math.max(180, sidebarWidth)); setSidebarWidth(64) }
    else setSidebarWidth(expandedSidebarWidth)
  }
  const navigateSettings = (section: SettingsView) => navigate(`/repos/${encodeURIComponent(repoId)}/settings/${section}`)

  return <div className="flex h-screen min-h-[560px] overflow-hidden bg-page text-ink">
    <div className="shrink-0 transition-[width] duration-150 motion-reduce:transition-none max-sm:hidden" style={{ width: sidebarWidth }}><ProjectSidebar fill project={repoId} active={active} settingsSection={settingsSection} collapsed={sidebarCollapsed} onCollapsedChange={setSidebarCollapsed} onNavigate={(view) => navigate(pathFor(repoId, view))} onSettingsNavigate={navigateSettings} /></div>
    <ResizeHandle label="Resize navigation sidebar" value={sidebarWidth} min={64} max={360} defaultValue={216} onChange={resizeSidebar} className="max-sm:hidden" />
    <main className="flex min-w-0 flex-1 flex-col"><div key={active} className="regent-view flex min-h-0 flex-1"><Routes>
      <Route path="sessions/:sessionId?" element={<SessionsScreen repoId={repoId} />} />
      <Route path="conversations/:sessionId?" element={<LegacySessionRedirect repoId={repoId} />} />
      <Route path="team" element={<TeamDashboard repoId={repoId} />} />
      <Route path="files" element={<FilesScreen repoId={repoId} />} />
      <Route path="skills" element={<SkillsScreen />} />
      <Route path="sync" element={<Navigate replace to={`/repos/${encodeURIComponent(repoId)}/settings/status`} />} />
      <Route path="settings/:section?" element={<SettingsRoute repoId={repoId} />} />
      <Route index element={<Navigate replace to="sessions" />} />
    </Routes></div><nav className="hidden h-11 shrink-0 items-center justify-around border-t border-line bg-canvas max-sm:flex">{(['sessions', 'team', 'files', 'skills', 'settings'] as RegentView[]).map((item) => <button key={item} onClick={() => navigate(pathFor(repoId, item))} className={`px-2 text-[11px] capitalize ${active === item ? 'text-accent-ink' : 'text-ink-3'}`}>{item}</button>)}</nav></main>
  </div>
}

function LegacySessionRedirect({ repoId }: { repoId: string }) {
  const { sessionId } = useParams()
  return <Navigate replace to={`/repos/${repoId}/sessions${sessionId ? `/${encodeURIComponent(sessionId)}` : ''}`} />
}

function SessionsScreen({ repoId }: { repoId: string }) {
  const { sessionId: routeSessionId } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  const [listWidth, setListWidth] = usePersistentPanelSize('sessions-list', 340, 260, 560)
  // Set when arriving from a blamed line in Browse: the step to scroll to and open.
  const focusStep = new URLSearchParams(location.search).get('step') || undefined
  const sessions = useQuery({ queryKey: ['sessions', repoId], queryFn: () => api.sessions(repoId), retry: false })
  const memberFor = (session: NonNullable<typeof sessions.data>['sessions'][number]) => session.author?.name || session.author?.email || 'Unknown author'
  const visibleSessions = sessions.data?.sessions ?? []
  const sessionId = routeSessionId && visibleSessions.some((session) => session.session_id === routeSessionId) ? routeSessionId : visibleSessions[0]?.session_id
  const transcript = useQuery({ queryKey: ['transcript', repoId, sessionId], queryFn: async () => { try { return { kind: 'transcript' as const, data: await api.transcript(repoId, sessionId!) } } catch (error) { if (error instanceof ApiError && error.status === 404) return { kind: 'log' as const, data: await api.log(repoId, sessionId!) }; throw error } }, enabled: Boolean(sessionId), retry: false, refetchInterval: 7_500 })
  if (sessions.isPending) return <Pending />
  if (sessions.error) return <Problem error={sessions.error} onRetry={() => sessions.refetch()} />
  const entries = transcript.data ? transcript.data.kind === 'transcript' ? transcriptToEntries(transcript.data.data) : logToTranscript(transcript.data.data) : []
  const selected = sessions.data?.sessions.find((item) => item.session_id === sessionId)
  return <div className="flex min-h-0 flex-1 max-sm:flex-col">
    <div className="min-h-0 shrink-0 overflow-hidden max-sm:!h-[42%] max-sm:!w-full" style={{ width: listWidth }}><SessionSearch sessions={visibleSessions} selectedId={sessionId} onSelect={(id) => navigate(`/repos/${encodeURIComponent(repoId)}/sessions/${encodeURIComponent(id)}`)} /></div>
    <ResizeHandle label="Resize session list" value={listWidth} min={260} max={560} defaultValue={340} onChange={setListWidth} className="max-sm:hidden" />
    <section key={sessionId || 'empty'} className="regent-view min-h-0 min-w-0 flex-1 overflow-auto bg-canvas">
      {!sessionId ? <div className="flex min-h-full"><Empty title="No captured sessions yet" detail="Initialize this repository with rgt, enable the agent hooks, and complete one tool-using turn." /></div>
        : <><div className="sticky top-0 z-10 flex min-h-[56px] items-center gap-3 border-b border-line bg-canvas/95 px-4 py-2 backdrop-blur"><div className="min-w-0 flex-1"><h1 className="m-0 truncate text-[14px] font-semibold leading-5">{selected?.title || 'Captured session'}</h1><div className="flex flex-wrap items-center gap-x-2 text-[10.5px] leading-4 text-ink-3"><span className="font-mono text-accent-ink">{sessionId}</span><span className="inline-flex items-center gap-1"><span className="inline-flex" style={{ color: agentColor(selected?.agent_id) }}><AgentIcon origin={selected?.agent_id} decorative className="size-3.5" /></span>{agentLabel(selected?.agent_id)}</span><span>{selected?.author ? memberFor(selected) : 'Unknown author'}</span><span>{selected?.step_count ?? entries.filter((item) => item.type === 'step').length} steps</span></div></div></div>
          {transcript.isPending ? <div className="flex min-h-[360px]"><Pending label="Reconstructing transcript…" /></div>
            : transcript.error ? <div className="flex min-h-[360px]"><Problem error={transcript.error} onRetry={() => transcript.refetch()} /></div>
              : entries.length ? <ConversationTranscript entries={entries} focusStep={focusStep} repoId={repoId} /> : <div className="flex min-h-[360px]"><Empty title="No transcript content" detail="The session exists, but its recorded steps do not contain readable conversation events." /></div>}</>}
    </section>
  </div>
}

function useLatestLog(repoId: string) {
  const sessions = useQuery({ queryKey: ['sessions', repoId], queryFn: () => api.sessions(repoId), retry: false })
  const session = sessions.data?.sessions[0]?.session_id
  const log = useQuery({ queryKey: ['log', repoId, session], queryFn: () => api.log(repoId, session!), enabled: Boolean(session), retry: false })
  return { sessions, session, log }
}

function FilesScreen({ repoId }: { repoId: string }) {
  const location = useLocation(); const params = new URLSearchParams(location.search)
  const requestedStep = params.get('step') || undefined; const requestedPath = params.get('path') || undefined
  const [treeWidth, setTreeWidth] = usePersistentPanelSize('files-tree', 320, 240, 560)
  const { sessions, session, log } = useLatestLog(repoId); const step = requestedStep || log.data?.steps[0]?.hash
  const files = useQuery({ queryKey: ['files', repoId, step, session], queryFn: () => api.files(repoId, { step, session }), enabled: Boolean(step || session), retry: false })
  const [selected, setSelected] = useState<string>()
  // A fresh ?path= link must win over whatever the user last clicked in the tree.
  useEffect(() => { setSelected(undefined) }, [requestedPath, requestedStep])
  const path = selected || requestedPath || files.data?.files[0]?.path
  // Shares the ['step', ...] cache with BlameView, so linking back to the session that
  // produced this tree costs no extra request.
  const stepDetail = useQuery({ queryKey: ['step', repoId, step], queryFn: () => api.step(repoId, step!), enabled: Boolean(step), retry: false })
  const stepSession = stepDetail.data?.session_id
  const blame = useQuery({ queryKey: ['blame', repoId, files.data?.step_hash, path], queryFn: () => api.blame(repoId, files.data!.step_hash, path!), enabled: Boolean(files.data?.step_hash && path), retry: false })
  if (sessions.isPending || (session && log.isPending) || ((step || session) && files.isPending)) return <Pending label="Reading captured tree…" />
  const error = sessions.error || log.error || files.error; if (error) return <Problem error={error} onRetry={() => files.refetch()} />
  if (!files.data?.files.length) return <Empty title="No captured files" detail="Choose a step with a workspace tree, or complete a captured agent turn first." />
  return <div className="flex min-h-0 flex-1 max-md:flex-col"><aside className="min-h-0 shrink-0 overflow-auto bg-canvas max-md:!h-[38%] max-md:!w-full" style={{ width: treeWidth }}><div className="sticky top-0 z-10 flex h-10 items-center border-b border-line bg-canvas px-3 text-[12.5px] font-semibold">Files<span className="ml-auto text-[10.5px] font-normal tabular-nums text-ink-3">{files.data.total_files}</span></div><FileTree files={files.data.files} selectedPath={path} onSelect={setSelected} /></aside><ResizeHandle label="Resize file tree" value={treeWidth} min={240} max={560} defaultValue={320} onChange={setTreeWidth} className="max-md:hidden" /><section className="flex min-w-0 flex-1 flex-col overflow-hidden bg-inset"><div className="z-10 flex h-10 shrink-0 items-center border-b border-line bg-canvas px-3"><span className="truncate font-mono text-[11.5px]">{path}</span>{path && <span className="ml-2 shrink-0 font-mono text-[10px] text-ink-3">{languageForPath(path)}</span>}{stepSession ? <Link to={`/repos/${encodeURIComponent(repoId)}/sessions/${encodeURIComponent(stepSession)}?step=${encodeURIComponent(files.data.step_hash)}`} aria-label={`Open the session that produced step ${short(files.data.step_hash)}`} className="ml-auto font-mono text-[10.5px] text-accent-ink underline-offset-2 hover:underline">{short(files.data.step_hash)}</Link> : <span className="ml-auto font-mono text-[10.5px] text-accent-ink">{short(files.data.step_hash)}</span>}</div>{blame.isPending ? <Pending label="Loading provenance…" /> : blame.error ? <Problem error={blame.error} onRetry={() => blame.refetch()} /> : <BlameView repoId={repoId} data={blame.data!} />}</section></div>
}

function StatusScreen({ repoId }: { repoId: string }) {
  const status = useQuery({ queryKey: ['status', repoId], queryFn: () => api.status(repoId), retry: false, refetchInterval: 10_000 })
  if (status.isPending) return <Pending label="Checking server…" />
  if (status.error) return <Problem error={status.error} onRetry={() => status.refetch()} />
  const data: StatusResponse = status.data; const service = typeof data.service === 'string' ? data.service : [data.service.name || 're_gent', apiVersionOf(data)].filter(Boolean).join(' · '); const repo = data.repository || {}
  const rows = [['Repository', repo.id || repoId], ['Service', service], ['Objects', repo.object_count ?? '—'], ['Refs', repo.ref_count ?? '—'], ['Sessions', repo.session_count ?? '—'], ['Last activity', repo.last_activity ? new Date(repo.last_activity).toLocaleString() : '—']]
  return <section className="mx-auto w-full max-w-[720px] p-5"><div className="mb-3 flex items-start justify-between"><div><h1 className="m-0 text-[16px] font-semibold leading-5">Server status</h1><p className="m-0 text-[11.5px] leading-4 text-ink-3">Repository storage and capture availability.</p></div><span className={`flex items-center gap-1.5 text-[11.5px] ${data.status === 'ok' ? 'text-green' : 'text-red'}`}><span className={`size-1.5 rounded-full ${data.status === 'ok' ? 'bg-green' : 'bg-red'}`} />{data.status}</span></div><div className="overflow-hidden rounded-[8px] border border-line">{rows.map(([label, value]) => <div key={label} className="grid grid-cols-[130px_1fr] border-b border-line text-[12px] last:border-0"><div className="bg-inset px-3 py-2 text-ink-3">{label}</div><div className="px-3 py-2 font-mono text-ink-2">{value}</div></div>)}</div></section>
}

function SettingsRoute({ repoId }: { repoId: string }) {
  const { section = 'general' } = useParams()
  if (section === 'status') return <StatusScreen repoId={repoId} />
  if (!['general', 'users', 'data'].includes(section)) return <Navigate replace to={`/repos/${encodeURIComponent(repoId)}/settings/general`} />
  return <SettingsScreen section={section as SettingsSection} />
}

export default function App() { return <Routes><Route path="/" element={<RepoHome />} /><Route path="/repos/:repoId/*" element={<Shell />} /><Route path="*" element={<Navigate replace to="/" />} /></Routes> }
