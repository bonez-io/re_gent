import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { useQueries, useQuery } from '@tanstack/react-query'
import { ApiError, api } from '../api/client'
import type { LogCause, SessionSummary, TranscriptResponse, TranscriptStep } from '../api/types'

type TimeRange = '7d' | '30d' | 'all'
type RankMetric = 'sessions' | 'toolCalls' | 'tokens' | 'steps' | 'cacheReadTokens' | 'activeDays' | 'uniqueTools' | 'toolSuccessRate'

type TeamMember = {
  id: string
  name: string
  email?: string
  sessions: number
  steps: number
  tokens: number
  cacheReadTokens: number
  toolCalls: number
  knownToolCalls: number
  successfulToolCalls: number
  activeDays: Set<string>
  uniqueTools: Set<string>
  agents: Set<string>
  agentSteps: Record<string, number>
  lastActivity?: string
}

type Achievement = { label: string; detail: string; rank: number; metric: RankMetric }

const viewerIsAdmin = true
const ranges: Array<{ key: TimeRange; label: string; suffix: string; cutoffDays?: number }> = [
  { key: '7d', label: '7D', suffix: 'last 7 days', cutoffDays: 7 },
  { key: '30d', label: '30D', suffix: 'last 30 days', cutoffDays: 30 },
  { key: 'all', label: 'All time', suffix: 'all time' },
]
const minimumSuccessSample = 5

const metricCatalog: Record<RankMetric, {
  label: string
  noun: string
  description: string
  value: (member: TeamMember) => number
  format: (value: number, member: TeamMember) => string
  qualifies?: (member: TeamMember) => boolean
}> = {
  sessions: {
    label: 'Most Sessions',
    noun: 'captured sessions',
    description: 'Captured sessions started by this teammate.',
    value: (member) => member.sessions,
    format: (value) => `${value.toLocaleString()} ${value === 1 ? 'session' : 'sessions'}`,
  },
  toolCalls: {
    label: 'Most Tool Calls',
    noun: 'recorded calls',
    description: 'Recorded tool calls across captured steps.',
    value: (member) => member.toolCalls,
    format: (value) => `${value.toLocaleString()} tool calls`,
  },
  tokens: {
    label: 'Most Tokens',
    noun: 'input + output',
    description: 'Input plus output tokens recorded in transcript usage.',
    value: (member) => member.tokens,
    format: (value) => `${formatCompact(value)} tokens`,
  },
  steps: {
    label: 'Most Steps',
    noun: 'captured steps',
    description: 'Captured agent steps across sessions.',
    value: (member) => member.steps,
    format: (value) => `${value.toLocaleString()} steps`,
  },
  cacheReadTokens: {
    label: 'Most Cache Reads',
    noun: 'cache-read usage',
    description: 'Cache-read tokens reported by transcript usage.',
    value: (member) => member.cacheReadTokens,
    format: (value) => `${formatCompact(value)} cache-read tokens`,
  },
  activeDays: {
    label: 'Most Active Days',
    noun: 'days with activity',
    description: 'Distinct days with captured session activity.',
    value: (member) => member.activeDays.size,
    format: (value) => `${value.toLocaleString()} active ${value === 1 ? 'day' : 'days'}`,
  },
  uniqueTools: {
    label: 'Most Tools Used',
    noun: 'distinct tool names',
    description: 'Distinct tool names recorded in transcript or log causes.',
    value: (member) => member.uniqueTools.size,
    format: (value) => `${value.toLocaleString()} unique ${value === 1 ? 'tool' : 'tools'}`,
  },
  toolSuccessRate: {
    label: 'Highest Tool Success',
    noun: 'classified successful calls',
    description: `Successful tool-call rate, shown only after ${minimumSuccessSample}+ classified tool calls.`,
    value: (member) => member.knownToolCalls ? member.successfulToolCalls / member.knownToolCalls : 0,
    format: (value, member) => member.knownToolCalls >= minimumSuccessSample ? formatPercent(value) : 'Not enough data',
    qualifies: (member) => member.knownToolCalls >= minimumSuccessSample,
  },
}

function formatCompact(value: number) {
  return new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: value >= 1000 ? 1 : 0 }).format(value)
}

function formatPercent(value: number) {
  return new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 1 }).format(value)
}

function memberId(author?: SessionSummary['author']) {
  return (author?.name || author?.email || 'unknown').toLowerCase()
}

function memberName(author?: SessionSummary['author']) {
  return author?.name || author?.email || 'Unknown author'
}

function inRange(iso: string | undefined, range: TimeRange, now = Date.now()) {
  if (range === 'all' || !iso) return true
  const time = new Date(iso).getTime()
  if (Number.isNaN(time)) return true
  const days = ranges.find((item) => item.key === range)?.cutoffDays ?? 30
  return now - time <= days * 24 * 60 * 60 * 1000
}

function classifyToolResult(result: unknown): 'success' | 'failure' | undefined {
  if (result == null) return undefined
  if (result && typeof result === 'object' && 'status' in result) {
    const status = String((result as { status?: unknown }).status).toLowerCase()
    if (/(fail|error|timeout|cancel)/.test(status)) return 'failure'
    if (/(pass|success|complete|ok|patch|write|done)/.test(status)) return 'success'
  }
  const normalized = typeof result === 'string' ? result.toLowerCase() : JSON.stringify(result).toLowerCase()
  if (/(✗|failed|error|exception|timeout|cancelled)/.test(normalized)) return 'failure'
  return 'success'
}

function emptyMember(session: SessionSummary): TeamMember {
  return {
    id: memberId(session.author),
    name: memberName(session.author),
    email: session.author?.email,
    sessions: 0,
    steps: 0,
    tokens: 0,
    cacheReadTokens: 0,
    toolCalls: 0,
    knownToolCalls: 0,
    successfulToolCalls: 0,
    activeDays: new Set<string>(),
    uniqueTools: new Set<string>(),
    agents: new Set<string>(),
    agentSteps: {},
    lastActivity: undefined,
  }
}

function mergeAuthor(member: TeamMember, author?: SessionSummary['author']) {
  if (!member.email && author?.email) member.email = author.email
  if (member.name === 'Unknown author' && memberName(author) !== 'Unknown author') member.name = memberName(author)
}

function addToolCall(member: TeamMember, cause: LogCause) {
  member.toolCalls += 1
  if (cause.tool) member.uniqueTools.add(cause.tool)
  const result = classifyToolResult(cause.result)
  if (result) {
    member.knownToolCalls += 1
    if (result === 'success') member.successfulToolCalls += 1
  }
}

function addStep(member: TeamMember, step: TranscriptStep) {
  member.steps += 1
  member.tokens += (step.usage?.input_tokens ?? 0) + (step.usage?.output_tokens ?? 0)
  member.cacheReadTokens += step.usage?.cache_read_tokens ?? 0
  step.causes.forEach((cause) => addToolCall(member, cause))
}

function aggregateMembers(sessions: SessionSummary[], transcripts: Array<TranscriptResponse | null | undefined>, removed: Set<string>) {
  const members = new Map<string, TeamMember>()
  sessions.forEach((session, index) => {
    const id = memberId(session.author)
    if (removed.has(id)) return
    const member = members.get(id) ?? emptyMember(session)
    members.set(id, member)
    mergeAuthor(member, session.author)
    member.sessions += 1
    member.steps += session.step_count || 0
    if (session.agent_id) {
      member.agents.add(session.agent_id)
      member.agentSteps[session.agent_id] = (member.agentSteps[session.agent_id] ?? 0) + Math.max(1, session.step_count || 0)
    }
    if (session.last_activity) {
      member.activeDays.add(new Date(session.last_activity).toISOString().slice(0, 10))
      if (!member.lastActivity || new Date(session.last_activity).getTime() > new Date(member.lastActivity).getTime()) member.lastActivity = session.last_activity
    }
    const transcript = transcripts[index]
    if (transcript) {
      member.steps -= session.step_count || 0
      transcript.steps.forEach((step) => addStep(member, step))
    }
  })
  return [...members.values()]
}

function rankMembers(members: TeamMember[], metric: RankMetric) {
  const descriptor = metricCatalog[metric]
  return [...members].sort((a, b) => {
    const aQualified = descriptor.qualifies?.(a) ?? true
    const bQualified = descriptor.qualifies?.(b) ?? true
    if (aQualified !== bQualified) return aQualified ? -1 : 1
    const diff = descriptor.value(b) - descriptor.value(a)
    if (diff) return diff
    return a.name.localeCompare(b.name)
  })
}

function rankFor(member: TeamMember, members: TeamMember[], metric: RankMetric) {
  const descriptor = metricCatalog[metric]
  if (descriptor.qualifies?.(member) === false) return 0
  const value = descriptor.value(member)
  return members.filter((other) => (descriptor.qualifies?.(other) ?? true) && descriptor.value(other) > value).length + 1
}

function metricHasSpread(members: TeamMember[], metric: RankMetric) {
  const descriptor = metricCatalog[metric]
  const values = members.filter((member) => descriptor.qualifies?.(member) ?? true).map((member) => descriptor.value(member))
  return new Set(values).size > 1
}

function tiedRankCount(member: TeamMember, members: TeamMember[], metric: RankMetric) {
  const descriptor = metricCatalog[metric]
  const value = descriptor.value(member)
  return members.filter((other) => (descriptor.qualifies?.(other) ?? true) && descriptor.value(other) === value).length
}

function metricDetail(metric: RankMetric, member: TeamMember, rangeLabel: string) {
  if (metric === 'toolSuccessRate') return `${member.successfulToolCalls}/${member.knownToolCalls} classified tool calls · ${rangeLabel}`
  return `${metricCatalog[metric].format(metricCatalog[metric].value(member), member)} · ${rangeLabel}`
}

function achievementsFor(member: TeamMember, members: TeamMember[], rangeLabel: string): Achievement[] {
  if (members.length < 2) return []
  const metrics = Object.keys(metricCatalog) as RankMetric[]
  return metrics.flatMap((metric) => {
    const descriptor = metricCatalog[metric]
    if (!metricHasSpread(members, metric)) return []
    if (descriptor.value(member) <= 0 || descriptor.qualifies?.(member) === false) return []
    const rank = rankMembers(members, metric).findIndex((item) => item.id === member.id) + 1
    if (rank < 1 || rank > 3) return []
    return [{ rank, metric, label: `#${rank} ${descriptor.label}`, detail: metricDetail(metric, member, rangeLabel) }]
  }).sort((a, b) => a.rank - b.rank || metricPriority(a.metric) - metricPriority(b.metric)).slice(0, 3)
}

function metricPriority(metric: RankMetric) {
  return (['sessions', 'toolCalls', 'tokens', 'steps', 'cacheReadTokens', 'toolSuccessRate', 'activeDays', 'uniqueTools'] as RankMetric[]).indexOf(metric)
}

function PulseStat({ value, label }: { value: string; label: string }) {
  return <span className="flex min-w-0 items-baseline justify-center gap-2 border-r border-line/70 px-4 last:border-r-0 max-sm:justify-start max-sm:border-r-0 max-sm:px-0">
    <span className="shrink-0 text-[29px] font-semibold leading-8 tabular-nums text-ink">{value}</span>
    <span className="min-w-0 truncate text-[12px] font-medium text-ink-3">{label}</span>
  </span>
}

function splitMetricValue(value: string) {
  const [head, ...tail] = value.split(' ')
  return { head, tail: tail.join(' ') }
}

function AchievementPill({ achievement }: { achievement: Achievement }) {
  const top = achievement.rank === 1
  return <span title={achievement.detail} className={`inline-flex h-6 max-w-full items-center rounded-[3px] border px-2 text-[10.5px] font-medium shadow-hairline transition-colors hover:border-accent/50 ${top ? 'border-accent/35 bg-accent-tint text-accent-ink' : 'border-line bg-field text-ink-2'}`}>
    <span className="truncate">{achievement.label}</span>
  </span>
}

function secondaryStats(member: TeamMember, selectedMetric: RankMetric) {
  return [
    { metric: 'tokens' as RankMetric, text: `${formatCompact(member.tokens)} tokens` },
    { metric: 'toolCalls' as RankMetric, text: `${member.toolCalls.toLocaleString()} tools` },
    { metric: 'steps' as RankMetric, text: `${member.steps.toLocaleString()} steps` },
    { metric: 'sessions' as RankMetric, text: `${member.sessions.toLocaleString()} ${member.sessions === 1 ? 'session' : 'sessions'}` },
    { metric: 'cacheReadTokens' as RankMetric, text: `${formatCompact(member.cacheReadTokens)} cache reads` },
    { metric: 'uniqueTools' as RankMetric, text: `${member.uniqueTools.size} tool types` },
    { metric: 'activeDays' as RankMetric, text: `${member.activeDays.size} active ${member.activeDays.size === 1 ? 'day' : 'days'}` },
    ...(member.knownToolCalls >= minimumSuccessSample ? [{ metric: 'toolSuccessRate' as RankMetric, text: `${formatPercent(member.successfulToolCalls / member.knownToolCalls)} success` }] : []),
  ].filter((item) => item.metric !== selectedMetric && !item.text.startsWith('0 ')).slice(0, 3)
}

function harnessSummary(member: TeamMember) {
  const entries = Object.entries(member.agentSteps).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
  if (!entries.length) return ''
  if (entries.length === 1) return entries[0][0]
  const total = entries.reduce((sum, [, steps]) => sum + steps, 0)
  return entries.map(([agent, steps]) => `${agent} ${Math.round((steps / total) * 100)}%`).join(' - ')
}

function EmptyState({ rangeLabel }: { rangeLabel: string }) {
  return <div className="rounded-[8px] border border-line bg-canvas px-4 py-12 text-center shadow-card">
    <h2 className="m-0 text-[14px] font-semibold">No team activity in the {rangeLabel}.</h2>
    <p className="m-0 mt-1 text-[11.5px] text-ink-3">Captured sessions will appear here once teammates use re_gent in this repository.</p>
  </div>
}

function MemberRow({ member, selectedMetric, allMembers, rangeLabel, canRemove, onRemove }: {
  member: TeamMember
  selectedMetric: RankMetric
  allMembers: TeamMember[]
  rangeLabel: string
  canRemove: boolean
  onRemove: (member: TeamMember) => void
}) {
  const [menuOpen, setMenuOpen] = useState(false)
  const menuRef = useRef<HTMLDivElement>(null)
  const descriptor = metricCatalog[selectedMetric]
  const qualified = descriptor.qualifies?.(member) ?? true
  const displayRank = rankFor(member, allMembers, selectedMetric)
  const mainValue = descriptor.format(descriptor.value(member), member)
  const mainParts = splitMetricValue(mainValue)
  const achievements = achievementsFor(member, allMembers, rangeLabel)
  const stats = secondaryStats(member, selectedMetric)
  const hasSpread = metricHasSpread(allMembers, selectedMetric)
  const topRank = hasSpread && displayRank <= 3 && qualified
  const tied = qualified && tiedRankCount(member, allMembers, selectedMetric) > 1

  useEffect(() => {
    if (!menuOpen) return
    const closeOnOutsideClick = (event: PointerEvent) => {
      if (menuRef.current?.contains(event.target as Node)) return
      setMenuOpen(false)
    }
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMenuOpen(false)
    }
    document.addEventListener('pointerdown', closeOnOutsideClick)
    document.addEventListener('keydown', closeOnEscape)
    return () => {
      document.removeEventListener('pointerdown', closeOnOutsideClick)
      document.removeEventListener('keydown', closeOnEscape)
    }
  }, [menuOpen])

  return <article data-testid="team-member-row" className={`relative grid gap-3 rounded-[8px] border px-3 py-2.5 transition-all duration-150 ${topRank ? 'border-accent/25 bg-canvas shadow-card' : 'border-line/70 bg-canvas/70'} hover:border-ink-3/40 md:grid-cols-[48px_minmax(260px,1fr)_minmax(150px,190px)_minmax(360px,1.35fr)_32px] md:items-center md:gap-x-5`}>
    <div className="flex min-w-0 items-center gap-2.5 md:contents">
      <span title={tied ? `Tied #${displayRank}` : undefined} className={`flex h-7 min-w-8 shrink-0 items-center justify-center rounded-[4px] px-1.5 font-mono text-[11.5px] font-semibold shadow-hairline ${topRank ? 'bg-accent-tint text-accent-ink' : 'bg-field text-ink-2'}`}>{qualified ? `#${displayRank}` : '#–'}</span>
      <div className="min-w-0 md:col-start-2">
        <div className="flex min-w-0 items-center gap-1.5">
          <h2 className="m-0 truncate text-[14px] font-semibold leading-5 text-ink">{member.name}</h2>
        </div>
        <div className="mt-0.5 min-w-0 text-[10.5px] leading-4 text-ink-3">
          {member.email && <div className="truncate">{member.email}</div>}
          <div className="truncate">{harnessSummary(member)}</div>
        </div>
      </div>
    </div>

    <div title={descriptor.description} className="min-w-0 text-left md:col-start-3">
      <div className="truncate text-[10px] font-medium leading-3 text-ink-3">{rangeLabel}</div>
      <div className="truncate leading-7 tabular-nums text-ink">
        {mainValue === 'Not enough data' ? <span className="text-[15px] font-semibold">{mainValue}</span> : <>
          <span className="text-[23px] font-semibold">{mainParts.head}</span>
          {mainParts.tail && <span className="ml-1.5 text-[15px] font-semibold text-ink"> {mainParts.tail}</span>}
        </>}
      </div>
      <div className="-mt-0.5 truncate text-[10.5px] leading-3 text-ink-3">{descriptor.noun}</div>
    </div>

    <div className="min-w-0 md:col-start-4 md:border-l md:border-line/60 md:pl-5">
      <div className="mb-1 text-[10px] font-medium leading-3 text-ink-3">{rangeLabel} totals</div>
      <div className="grid gap-x-4 gap-y-1 text-[11px] leading-4 text-ink-3" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(86px, max-content))' }}>
        {stats.map((stat) => {
          const [value, ...label] = stat.text.split(' ')
          return <span key={stat.metric} className="whitespace-nowrap"><b className="font-semibold text-ink-2">{value}</b> {label.join(' ')}</span>
        })}
      </div>
      {achievements.length > 0 && <div className="mt-2 flex flex-wrap gap-1.5">{achievements.map((achievement) => <AchievementPill key={achievement.label} achievement={achievement} />)}</div>}
    </div>

    {canRemove && <div ref={menuRef} className="absolute right-2.5 top-2.5 md:relative md:right-auto md:top-auto md:col-start-5 md:justify-self-end">
      <button type="button" aria-label={`Team actions for ${member.name}`} onClick={() => setMenuOpen((open) => !open)} className={`flex size-7 items-center justify-center rounded-[4px] border text-[14px] leading-none shadow-hairline transition-colors ${menuOpen ? 'border-accent/35 bg-accent-tint text-accent-ink' : 'border-line/70 bg-canvas text-ink-3 hover:bg-field hover:text-ink'}`}>•••</button>
      {menuOpen && <div data-testid="team-actions-menu" className="absolute right-0 top-[calc(100%+6px)] z-20 w-48 overflow-hidden rounded-[8px] border border-line bg-surface shadow-overlay">
        <div className="border-b border-line px-2.5 py-2">
          <div className="truncate text-[11.5px] font-semibold text-ink">{member.name}</div>
          <div className="truncate text-[10.5px] text-ink-3">Team member</div>
        </div>
        <button type="button" onClick={() => { setMenuOpen(false); onRemove(member) }} className="flex h-9 w-full items-center px-2.5 text-left text-[11.5px] font-medium text-red hover:bg-red-tint">Remove from team</button>
      </div>}
    </div>}
  </article>
}

export function TeamDashboard({ repoId }: { repoId: string }) {
  const [timeRange, setTimeRange] = useState<TimeRange>(() => (localStorage.getItem('regent-team-range') as TimeRange | null) || '30d')
  const [selectedMetric, setSelectedMetric] = useState<RankMetric>(() => (localStorage.getItem('regent-team-metric') as RankMetric | null) || 'sessions')
  const [inviteOpen, setInviteOpen] = useState(false)
  const [email, setEmail] = useState('')
  const [sentInvites, setSentInvites] = useState<string[]>([])
  const [removedIds, setRemovedIds] = useState<Set<string>>(() => new Set())
  const [query, setQuery] = useState('')
  const sessionsQuery = useQuery({ queryKey: ['sessions', repoId], queryFn: () => api.sessions(repoId), retry: false })
  const range = ranges.find((item) => item.key === timeRange) ?? ranges[1]
  const visibleSessions = useMemo(() => (sessionsQuery.data?.sessions ?? []).filter((session) => inRange(session.last_activity, timeRange)), [sessionsQuery.data, timeRange])
  const transcriptQueries = useQueries({
    queries: visibleSessions.map((session) => ({
      queryKey: ['team-transcript', repoId, session.session_id],
      queryFn: async () => {
        try { return await api.transcript(repoId, session.session_id) }
        catch (error) { if (error instanceof ApiError && error.status === 404) return null; throw error }
      },
      retry: false,
      staleTime: 15_000,
    })),
  })
  const transcriptData = transcriptQueries.map((item) => item.data)
  const members = useMemo(() => aggregateMembers(visibleSessions, transcriptData, removedIds), [visibleSessions, transcriptData, removedIds])
  const filteredMembers = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return needle ? members.filter((member) => `${member.name} ${member.email ?? ''} ${[...member.agents].join(' ')}`.toLowerCase().includes(needle)) : members
  }, [members, query])
  const ranked = useMemo(() => rankMembers(filteredMembers, selectedMetric), [filteredMembers, selectedMetric])
  const totals = useMemo(() => members.reduce((acc, member) => ({
    sessions: acc.sessions + member.sessions,
    steps: acc.steps + member.steps,
    tokens: acc.tokens + member.tokens,
    toolCalls: acc.toolCalls + member.toolCalls,
  }), { sessions: 0, steps: 0, tokens: 0, toolCalls: 0 }), [members])
  const loadingDetails = transcriptQueries.some((item) => item.isPending)
  const canSearch = members.length > 8

  const changeRange = (next: TimeRange) => {
    setTimeRange(next)
    localStorage.setItem('regent-team-range', next)
  }

  const changeMetric = (next: RankMetric) => {
    setSelectedMetric(next)
    localStorage.setItem('regent-team-metric', next)
  }

  const invite = (event: FormEvent) => {
    event.preventDefault()
    const clean = email.trim().toLowerCase()
    if (!clean) return
    setSentInvites((current) => current.includes(clean) ? current : [clean, ...current])
    setEmail('')
    setInviteOpen(false)
  }

  const removeMember = (member: TeamMember) => {
    if (!window.confirm(`Remove ${member.name} from this local team view?`)) return
    setRemovedIds((current) => new Set([...current, member.id]))
  }

  if (sessionsQuery.isPending) return <section className="flex flex-1 items-center justify-center bg-page text-[12px] text-ink-3"><span className="mr-2 size-2 animate-pulse rounded-full bg-accent" />Loading team activity…</section>
  if (sessionsQuery.error) return <section className="m-auto max-w-sm px-6 py-10 text-center"><h2 className="m-0 text-[15px] font-semibold">Could not load team activity</h2><p className="mt-1 text-[12px] leading-5 text-ink-3">{sessionsQuery.error.message}</p><button onClick={() => sessionsQuery.refetch()} className="mt-3 h-8 rounded-[4px] bg-field px-3 text-[12px] shadow-hairline hover:bg-hover-2">Retry</button></section>

  return <section className="min-h-0 flex-1 overflow-auto bg-page">
    <div className="mx-auto max-w-[1180px] px-4 py-5">
      <header className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <h1 className="m-0 text-[22px] font-semibold leading-7">Team</h1>
        </div>
        {viewerIsAdmin && <button type="button" onClick={() => setInviteOpen(true)} className="h-8 rounded-[4px] bg-accent-tint px-3 text-[12px] font-semibold text-accent-ink shadow-hairline transition-colors hover:bg-hover-2">Invite teammate</button>}
      </header>

      <div className="mt-4 flex flex-wrap items-center gap-2">
        <div className="flex rounded-[8px] bg-canvas p-1 shadow-hairline">
          {ranges.map((item) => <button key={item.key} type="button" onClick={() => changeRange(item.key)} className={`h-7 rounded-[4px] px-2.5 text-[11px] font-medium transition-colors ${timeRange === item.key ? 'bg-accent-tint text-accent-ink shadow-hairline' : 'text-ink-3 hover:bg-field hover:text-ink-2'}`}>{item.label}</button>)}
        </div>
        <span className="text-[11px] text-ink-3">{range.suffix}</span>
        {canSearch && <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search teammates" className="ml-auto h-8 min-w-[220px] rounded-[4px] bg-field px-2.5 text-[12px] outline-none shadow-hairline focus:shadow-btn" />}
      </div>

      <div className="mt-2.5 grid gap-y-2 rounded-[8px] border border-line/70 bg-canvas px-2 py-3.5 shadow-card sm:grid-cols-5">
        <PulseStat value={members.length.toLocaleString()} label={members.length === 1 ? 'Teammate' : 'Teammates'} />
        <PulseStat value={formatCompact(totals.tokens)} label="Tokens" />
        <PulseStat value={formatCompact(totals.toolCalls)} label="Tool Calls" />
        <PulseStat value={totals.sessions.toLocaleString()} label="Sessions" />
        <PulseStat value={totals.steps.toLocaleString()} label="Steps" />
        {loadingDetails && <span className="col-span-full px-4 text-[10.5px] text-ink-3">Reading transcripts…</span>}
      </div>

      <section className="mt-5">
        <div className="flex flex-wrap items-baseline gap-2">
          <h2 className="m-0 text-[14px] font-semibold leading-5 text-ink">Activity</h2>
          <span className="text-[11px] font-medium text-ink-3">{range.suffix}</span>
        </div>
        <div className="mt-2.5 flex gap-2 overflow-x-auto pb-1">
        {(Object.keys(metricCatalog) as RankMetric[]).map((metric) => <button key={metric} type="button" title={metricCatalog[metric].description} onClick={() => changeMetric(metric)} className={`h-8 shrink-0 rounded-[3px] border px-3 text-[11.5px] font-medium transition-all ${selectedMetric === metric ? 'border-accent/35 bg-accent-tint text-accent-ink shadow-hairline' : 'border-line bg-canvas text-ink-3 hover:border-ink-3/50 hover:text-ink-2'}`}>{metricCatalog[metric].label}</button>)}
        </div>

        <div className="mt-4 space-y-2">
          {ranked.length ? ranked.map((member) => <MemberRow key={`${member.id}-${selectedMetric}`} member={member} selectedMetric={selectedMetric} allMembers={members} rangeLabel={range.suffix} canRemove={viewerIsAdmin} onRemove={removeMember} />) : <EmptyState rangeLabel={range.suffix} />}
        </div>
      </section>
    </div>

    {inviteOpen && <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/45 px-4 pt-[18vh]" role="dialog" aria-modal="true" aria-labelledby="team-invite-title">
      <form onSubmit={invite} className="w-full max-w-[380px] rounded-[8px] border border-line bg-canvas p-3 shadow-overlay">
        <div className="flex items-start gap-2.5">
          <div className="min-w-0 flex-1">
            <h2 id="team-invite-title" className="m-0 text-[14px] font-semibold">Invite teammate</h2>
            <p className="m-0 mt-0.5 text-[11px] leading-4 text-ink-3">Send access to this repository’s re_gent history.</p>
          </div>
          <button type="button" onClick={() => setInviteOpen(false)} aria-label="Close invite dialog" className="flex size-7 items-center justify-center rounded-[4px] text-ink-3 hover:bg-field hover:text-ink">×</button>
        </div>
        <div className="mt-3 flex gap-2">
          <input aria-label="Email" type="email" required autoFocus value={email} onChange={(event) => setEmail(event.target.value)} placeholder="dev@company.com" className="h-9 min-w-0 flex-1 rounded-[4px] bg-field px-2.5 text-[12px] outline-none shadow-hairline focus:shadow-btn" />
          <button className="h-9 rounded-[4px] bg-accent-tint px-3 text-[12px] font-semibold text-accent-ink shadow-hairline hover:bg-hover-2">Invite</button>
        </div>
        {sentInvites.length > 0 && <div className="mt-3 flex flex-wrap gap-1.5 border-t border-line pt-2">{sentInvites.map((inviteEmail) => <span key={inviteEmail} className="inline-flex h-6 max-w-full items-center gap-1.5 rounded-[4px] bg-field px-2 text-[10.5px] text-ink-2 shadow-hairline"><span className="size-1.5 shrink-0 rounded-full bg-accent" /><span className="min-w-0 truncate">{inviteEmail}</span><span className="text-ink-3">sent</span></span>)}</div>}
      </form>
    </div>}
  </section>
}
