import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom'
import { ApiError, OfflineError, api } from './api/client'
import { logToTranscript, sessionToConversation, transcriptToEntries } from './api/adapters'
import type { BlameResponse, StatusResponse } from './api/types'
import { ConversationTranscript } from './components/ConversationTranscript'
import { FileTree } from './components/FileTree'
import { ProjectSidebar, type RegentView } from './components/ProjectSidebar'
import { SessionRow } from './components/SessionRow'

const defaultRepo = import.meta.env.VITE_REGENT_REPO_ID as string | undefined
const short = (value?: string) => value ? value.slice(0, 8) : '—'
const viewFor = (path: string): RegentView => path.endsWith('/steps') ? 'steps' : path.endsWith('/files') ? 'files' : path.endsWith('/sync') ? 'sync' : 'sessions'
const pathFor = (repoId: string, view: RegentView) => `/repos/${encodeURIComponent(repoId)}/${view}`

function Pending({ label = 'Loading captured work…' }: { label?: string }) {
  return <div className="flex flex-1 items-center justify-center text-[12px] text-ink-3"><span className="mr-2 size-2 animate-pulse rounded-full bg-accent" />{label}</div>
}

function Problem({ error, onRetry }: { error: Error; onRetry?: () => void }) {
  const offline = error instanceof OfflineError
  const missing = error instanceof ApiError && error.status === 404
  return <div className="m-auto max-w-sm px-6 py-10 text-center"><div className={`mx-auto mb-2 size-2 rounded-full ${offline ? 'bg-red' : 'bg-accent'}`} /><h2 className="m-0 text-[15px] font-semibold">{offline ? 'Server disconnected' : missing ? 'Data is not available yet' : 'Could not load this view'}</h2><p className="mt-1 text-[12px] leading-5 text-ink-3">{offline ? 'Start the local re_gent server on 127.0.0.1:7654, then retry.' : error.message}</p>{onRetry && <button onClick={onRetry} className="mt-3 h-8 rounded-[7px] bg-field px-3 text-[12px] shadow-hairline hover:bg-hover-2">Retry</button>}</div>
}

function Empty({ title, detail }: { title: string; detail: string }) {
  return <div className="m-auto max-w-md px-6 py-12 text-center"><img src="/favicon.svg" alt="" className="mx-auto mb-2 size-8 opacity-70" /><h2 className="m-0 text-[15px] font-semibold">{title}</h2><p className="mt-1 text-[12px] leading-5 text-ink-3">{detail}</p></div>
}

function RepoHome() {
  const navigate = useNavigate(); const queryClient = useQueryClient(); const [repoId, setRepoId] = useState('')
  const repos = useQuery({ queryKey: ['repos'], queryFn: api.listRepos, retry: false })
  const create = useMutation({ mutationFn: api.createRepo, onSuccess: async (repo) => { await queryClient.invalidateQueries({ queryKey: ['repos'] }); navigate(`/repos/${repo.repo_id}/sessions`) } })
  if (repos.isPending) return <main className="flex min-h-screen bg-page text-ink"><Pending label="Connecting to re_gent…" /></main>
  if (repos.error) return <main className="flex min-h-screen bg-page text-ink"><Problem error={repos.error} onRetry={() => repos.refetch()} /></main>
  if (defaultRepo && repos.data.repos.includes(defaultRepo)) return <Navigate replace to={`/repos/${defaultRepo}/sessions`} />
  if (repos.data.repos.length === 1) return <Navigate replace to={`/repos/${repos.data.repos[0]}/sessions`} />
  return <main className="flex min-h-screen items-center justify-center bg-page p-4 text-ink"><section className="w-full max-w-md overflow-hidden rounded-[11px] border border-line bg-canvas shadow-raised"><header className="flex items-center gap-2 border-b border-line px-4 py-2.5"><img src="/favicon.svg" alt="" className="size-7" /><div><h1 className="m-0 text-[15px] font-semibold leading-5">Open a re_gent repository</h1><p className="m-0 text-[11px] leading-4 text-ink-3">Repositories registered on this server</p></div></header>{repos.data.repos.length > 0 && <div className="p-2">{repos.data.repos.map((repo) => <button key={repo} onClick={() => navigate(`/repos/${repo}/sessions`)} className="flex h-10 w-full items-center rounded-[7px] px-2.5 text-left text-[12.5px] hover:bg-hover"><span className="size-1.5 rounded-full bg-green" /><span className="ml-2 flex-1 font-medium">{repo}</span><span className="text-ink-3">Open →</span></button>)}</div>}<form onSubmit={(event) => { event.preventDefault(); if (repoId) create.mutate(repoId) }} className="border-t border-line p-3"><label className="mb-1 block text-[11px] font-medium text-ink-3">Register empty server repository</label><div className="flex gap-2"><input required pattern="[a-z0-9][a-z0-9._-]{0,63}" value={repoId} onChange={(event) => setRepoId(event.target.value.toLowerCase())} placeholder="girlfriend-assistant" className="h-8 min-w-0 flex-1 rounded-[7px] bg-field px-2.5 text-[12px] outline-none shadow-hairline focus:shadow-btn" /><button disabled={create.isPending} className="h-8 rounded-[7px] bg-accent-tint px-3 text-[12px] font-medium text-accent-ink shadow-hairline disabled:opacity-50">{create.isPending ? 'Registering…' : 'Register'}</button></div>{create.error && <p className="mb-0 text-[11px] text-red">{create.error.message}</p>}<p className="mb-0 mt-1.5 text-[10.5px] leading-4 text-ink-3">Registration does not wire a working copy. In the initialized repo, run <code className="font-mono text-ink-2">rgt connect http://127.0.0.1:7654 --as &lt;repo-id&gt;</code>, then restart the agent.</p></form></section></main>
}

function Topbar({ repoId }: { repoId: string }) {
  const active = viewFor(useLocation().pathname)
  const labels: Record<RegentView, string> = { sessions: 'Sessions', steps: 'Steps', files: 'Files', sync: 'Server status' }
  return <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line bg-canvas px-3"><span className="text-[11.5px] text-ink-3">{repoId}</span><span className="text-ink-3/50">/</span><span className="text-[12.5px] font-medium">{labels[active]}</span><span className="ml-auto flex items-center gap-1.5 text-[11px] text-ink-3"><span className="size-1.5 rounded-full bg-green" />local server</span></header>
}

function Shell() {
  const { repoId = '' } = useParams(); const navigate = useNavigate(); const location = useLocation(); const active = viewFor(location.pathname)
  const sessions = useQuery({ queryKey: ['sessions', repoId], queryFn: () => api.sessions(repoId), retry: false })
  return <div className="flex h-screen min-h-[560px] overflow-hidden bg-page text-ink"><div className="shrink-0 max-sm:hidden"><ProjectSidebar project={repoId} conversationCount={sessions.data?.total_sessions ?? 0} active={active} onProjectClick={() => navigate('/')} onNavigate={(view) => navigate(pathFor(repoId, view))} /></div><main className="flex min-w-0 flex-1 flex-col"><Topbar repoId={repoId} /><div key={location.pathname} className="regent-view flex min-h-0 flex-1"><Routes><Route path="sessions" element={<SessionsScreen repoId={repoId} />} /><Route path="sessions/:sessionId" element={<SessionsScreen repoId={repoId} />} /><Route path="conversations" element={<LegacySessionRedirect repoId={repoId} />} /><Route path="conversations/:sessionId" element={<LegacySessionRedirect repoId={repoId} />} /><Route path="steps" element={<StepsScreen repoId={repoId} />} /><Route path="files" element={<FilesScreen repoId={repoId} />} /><Route path="sync" element={<StatusScreen repoId={repoId} />} /><Route index element={<Navigate replace to="sessions" />} /></Routes></div><nav className="hidden h-11 shrink-0 items-center justify-around border-t border-line bg-canvas max-sm:flex">{(['sessions', 'steps', 'files', 'sync'] as RegentView[]).map((item) => <button key={item} onClick={() => navigate(pathFor(repoId, item))} className={`px-2 text-[11px] capitalize ${active === item ? 'text-accent-ink' : 'text-ink-3'}`}>{item}</button>)}</nav></main></div>
}

function LegacySessionRedirect({ repoId }: { repoId: string }) {
  const { sessionId } = useParams()
  return <Navigate replace to={`/repos/${repoId}/sessions${sessionId ? `/${encodeURIComponent(sessionId)}` : ''}`} />
}

function SessionsScreen({ repoId }: { repoId: string }) {
  const { sessionId: routeSessionId } = useParams(); const navigate = useNavigate(); const [query, setQuery] = useState('')
  const sessions = useQuery({ queryKey: ['sessions', repoId], queryFn: () => api.sessions(repoId), retry: false })
  const sessionId = routeSessionId || sessions.data?.sessions[0]?.session_id
  const rows = useMemo(() => (sessions.data?.sessions ?? []).map(sessionToConversation).filter((item) => `${item.title} ${item.agent} ${item.author}`.toLowerCase().includes(query.toLowerCase())), [sessions.data, query])
  const transcript = useQuery({ queryKey: ['transcript', repoId, sessionId], queryFn: async () => { try { return { kind: 'transcript' as const, data: await api.transcript(repoId, sessionId!) } } catch (error) { if (error instanceof ApiError && error.status === 404) return { kind: 'log' as const, data: await api.log(repoId, sessionId!) }; throw error } }, enabled: Boolean(sessionId), retry: false, refetchInterval: 7_500 })
  if (sessions.isPending) return <Pending />
  if (sessions.error) return <Problem error={sessions.error} onRetry={() => sessions.refetch()} />
  if (!sessionId) return <Empty title="No captured sessions yet" detail="Initialize this repository with rgt, enable the agent hooks, and complete one tool-using turn." />
  if (transcript.isPending) return <Pending label="Reconstructing transcript…" />
  if (transcript.error) return <Problem error={transcript.error} onRetry={() => transcript.refetch()} />
  const entries = transcript.data.kind === 'transcript' ? transcriptToEntries(transcript.data.data) : logToTranscript(transcript.data.data)
  const selected = sessions.data?.sessions.find((item) => item.session_id === sessionId)
  return <div className="grid min-h-0 flex-1 grid-cols-[290px_minmax(0,1fr)] max-sm:grid-cols-1"><aside className="min-h-0 overflow-auto border-r border-line bg-canvas max-sm:max-h-56 max-sm:border-b max-sm:border-r-0"><div className="sticky top-0 z-10 border-b border-line bg-canvas px-2.5 py-2"><div className="flex items-center text-[13px] font-semibold leading-4">Sessions<span className="ml-auto text-[10.5px] font-normal tabular-nums text-ink-3">{sessions.data.total_sessions}</span></div><input value={query} onChange={(event) => setQuery(event.target.value)} className="mt-1.5 h-7 w-full rounded-[7px] bg-field px-2 text-[11.5px] outline-none shadow-hairline focus:shadow-btn" placeholder="Filter sessions…" /></div>{rows.length ? rows.map((row) => <SessionRow key={row.id} {...row} selected={row.id === sessionId} onClick={() => navigate(`/repos/${repoId}/sessions/${encodeURIComponent(row.id)}`)} />) : <div className="px-3 py-5 text-center text-[11.5px] text-ink-3">No matching sessions</div>}</aside><section className="min-h-0 overflow-auto bg-canvas"><div className="sticky top-0 z-10 border-b border-line bg-canvas/95 px-3 py-1.5 backdrop-blur"><h1 className="m-0 truncate text-[14px] font-semibold leading-5">{selected?.title || 'Captured session'}</h1><div className="flex flex-wrap gap-x-2 text-[10.5px] leading-4 text-ink-3"><span className="font-mono text-accent-ink">{sessionId}</span><span>{selected?.agent_id}</span><span>{selected?.step_count ?? entries.filter((item) => item.type === 'step').length} steps</span></div></div>{entries.length ? <ConversationTranscript entries={entries} /> : <div className="flex min-h-[360px]"><Empty title="No transcript content" detail="The session exists, but its recorded steps do not contain readable conversation events." /></div>}</section></div>
}

function useLatestLog(repoId: string) {
  const sessions = useQuery({ queryKey: ['sessions', repoId], queryFn: () => api.sessions(repoId), retry: false })
  const session = sessions.data?.sessions[0]?.session_id
  const log = useQuery({ queryKey: ['log', repoId, session], queryFn: () => api.log(repoId, session!), enabled: Boolean(session), retry: false })
  return { sessions, session, log }
}

function StepsScreen({ repoId }: { repoId: string }) {
  const navigate = useNavigate(); const { sessions, session, log } = useLatestLog(repoId)
  if (sessions.isPending || (session && log.isPending)) return <Pending />
  if (sessions.error) return <Problem error={sessions.error} onRetry={() => sessions.refetch()} />
  if (log.error) return <Problem error={log.error} onRetry={() => log.refetch()} />
  if (!session) return <Empty title="No steps yet" detail="Captured steps appear here after the first tool-using agent turn." />
  return <section className="min-h-0 flex-1 overflow-auto bg-canvas p-4"><div className="mb-2.5"><h1 className="m-0 text-[16px] font-semibold leading-5">Steps</h1><p className="m-0 text-[11.5px] leading-4 text-ink-3">Latest session <span className="font-mono">{session}</span></p></div><div className="overflow-auto rounded-[8px] border border-line"><div className="grid h-8 min-w-[700px] grid-cols-[100px_100px_1fr_150px_70px] items-center bg-inset px-2.5 text-[10.5px] font-medium text-ink-3"><span>Step</span><span>Parent</span><span>Origin</span><span>Captured</span><span>Files</span></div>{(log.data?.steps ?? []).map((step) => <button key={step.hash} onClick={() => navigate(`/repos/${repoId}/files?step=${step.hash}`)} className="grid h-9 min-w-[700px] w-full grid-cols-[100px_100px_1fr_150px_70px] items-center border-t border-line px-2.5 text-left text-[11.5px] text-ink-3 hover:bg-hover"><span className="font-mono text-accent-ink">{short(step.hash)}</span><span className="font-mono">{short(step.parent)}</span><span>{step.origin}</span><time>{step.timestamp ? new Date(step.timestamp).toLocaleString() : '—'}</time><span>{step.files.length}</span></button>)}</div></section>
}

function FilesScreen({ repoId }: { repoId: string }) {
  const location = useLocation(); const requestedStep = new URLSearchParams(location.search).get('step') || undefined
  const { sessions, session, log } = useLatestLog(repoId); const step = requestedStep || log.data?.steps[0]?.hash
  const files = useQuery({ queryKey: ['files', repoId, step, session], queryFn: () => api.files(repoId, { step, session }), enabled: Boolean(step || session), retry: false })
  const [selected, setSelected] = useState<string>(); const path = selected || files.data?.files[0]?.path
  const blame = useQuery({ queryKey: ['blame', repoId, files.data?.step_hash, path], queryFn: () => api.blame(repoId, files.data!.step_hash, path!), enabled: Boolean(files.data?.step_hash && path), retry: false })
  if (sessions.isPending || (session && log.isPending) || ((step || session) && files.isPending)) return <Pending label="Reading captured tree…" />
  const error = sessions.error || log.error || files.error; if (error) return <Problem error={error} onRetry={() => files.refetch()} />
  if (!files.data?.files.length) return <Empty title="No captured files" detail="Choose a step with a workspace tree, or complete a captured agent turn first." />
  return <div className="grid min-h-0 flex-1 grid-cols-[300px_minmax(0,1fr)] max-md:grid-cols-1"><aside className="overflow-auto border-r border-line bg-canvas max-md:max-h-64 max-md:border-b max-md:border-r-0"><div className="sticky top-0 z-10 flex h-9 items-center border-b border-line bg-canvas px-2.5 text-[12.5px] font-semibold">Files<span className="ml-auto text-[10.5px] font-normal tabular-nums text-ink-3">{files.data.total_files}</span></div><FileTree files={files.data.files} selectedPath={path} onSelect={setSelected} /></aside><section className="min-w-0 overflow-auto bg-inset"><div className="sticky top-0 z-10 flex h-9 items-center border-b border-line bg-canvas px-3"><span className="truncate font-mono text-[11.5px]">{path}</span><span className="ml-auto font-mono text-[10.5px] text-accent-ink">{short(files.data.step_hash)}</span></div>{blame.isPending ? <Pending label="Loading provenance…" /> : blame.error ? <Problem error={blame.error} onRetry={() => blame.refetch()} /> : <BlameCode data={blame.data!} />}</section></div>
}

function BlameCode({ data }: { data: BlameResponse }) {
  return <div className="overflow-auto py-2 font-mono text-[11.5px] leading-7">{data.lines.map((line) => <div key={line.number} className="grid min-w-[760px] grid-cols-[78px_90px_42px_minmax(0,1fr)] border-l-2 border-transparent px-2 hover:border-accent hover:bg-hover"><span className="text-accent-ink">{short(line.step_hash)}</span><span className="truncate text-ink-3">{line.origin || 'unknown'}</span><span className="select-none text-right text-ink-3">{line.number}</span><code className="whitespace-pre pl-3 text-ink-2">{line.content}</code></div>)}</div>
}

function StatusScreen({ repoId }: { repoId: string }) {
  const status = useQuery({ queryKey: ['status', repoId], queryFn: () => api.status(repoId), retry: false, refetchInterval: 10_000 })
  if (status.isPending) return <Pending label="Checking server…" />
  if (status.error) return <Problem error={status.error} onRetry={() => status.refetch()} />
  const data: StatusResponse = status.data; const service = typeof data.service === 'string' ? data.service : `${data.service.name || 're_gent'} ${data.service.api_version || ''}`.trim(); const repo = data.repository || {}
  const rows = [['Repository', repo.id || repoId], ['Service', service], ['Objects', repo.object_count ?? '—'], ['Refs', repo.ref_count ?? '—'], ['Sessions', repo.session_count ?? '—'], ['Last activity', repo.last_activity ? new Date(repo.last_activity).toLocaleString() : '—']]
  return <section className="mx-auto w-full max-w-[720px] p-5"><div className="mb-3 flex items-start justify-between"><div><h1 className="m-0 text-[16px] font-semibold leading-5">Server status</h1><p className="m-0 text-[11.5px] leading-4 text-ink-3">Repository storage and capture availability.</p></div><span className="flex items-center gap-1.5 text-[11.5px] text-green"><span className="size-1.5 rounded-full bg-green" />{data.status}</span></div><div className="overflow-hidden rounded-[8px] border border-line">{rows.map(([label, value]) => <div key={label} className="grid grid-cols-[130px_1fr] border-b border-line text-[12px] last:border-0"><div className="bg-inset px-3 py-2 text-ink-3">{label}</div><div className="px-3 py-2 font-mono text-ink-2">{value}</div></div>)}</div></section>
}

export default function App() { return <Routes><Route path="/" element={<RepoHome />} /><Route path="/repos/:repoId/*" element={<Shell />} /><Route path="*" element={<Navigate replace to="/" />} /></Routes> }
