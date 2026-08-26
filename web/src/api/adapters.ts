import type { Conversation, LogCause, LogResponse, SessionSummary, TranscriptEntry, TranscriptResponse } from './types'

function timeLabel(value: string) {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return value
  const delta = Date.now() - date.valueOf()
  if (delta < 60_000) return 'now'
  if (delta < 3_600_000) return `${Math.floor(delta / 60_000)}m`
  if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)}h`
  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

function dateGroup(value: string): Conversation['dateGroup'] {
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return 'Earlier'
  const today = new Date(); today.setHours(0, 0, 0, 0)
  const day = new Date(date); day.setHours(0, 0, 0, 0)
  const days = Math.round((today.valueOf() - day.valueOf()) / 86_400_000)
  return days <= 0 ? 'Today' : days === 1 ? 'Yesterday' : 'Earlier'
}

export function sessionToConversation(session: SessionSummary): Conversation {
  const origin = session.agent_id || session.session_id.split(':')[0] || 'unknown'
  return {
    id: session.session_id,
    title: session.title || 'Untitled captured session',
    author: session.author?.name || session.author?.email,
    agent: origin,
    branch: 'session ref',
    steps: session.step_count,
    files: 0,
    relativeTime: timeLabel(session.last_activity),
    dateGroup: dateGroup(session.last_activity),
    status: session.last_activity && Date.now() - new Date(session.last_activity).valueOf() < 120_000 ? 'capturing' : 'complete',
  }
}

function display(value: unknown): string[] {
  if (value == null) return []
  if (typeof value === 'string') return value.split('\n').slice(0, 8)
  try { return JSON.stringify(value, null, 2).split('\n').slice(0, 12) } catch { return [String(value)] }
}

function transcriptTime(value?: string) {
  return value ? new Date(value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) : ''
}

/** The repo-relative file a tool call names, resolved against the files the step actually
 *  touched. Tool arguments carry absolute host paths ("/Users/…/repo/dummy.txt") while captured
 *  trees are repo-relative ("dummy.txt"), so the two only match by suffix. Returns nothing for a
 *  tool that names no file (Bash, Search) rather than guessing — attribution we do not have. */
function callFiles(args: unknown, stepFiles: string[]) {
  const record = args && typeof args === 'object' ? args as Record<string, unknown> : undefined
  const raw = record?.file_path ?? record?.path
  if (typeof raw !== 'string' || !raw) return undefined
  const match = stepFiles.find((file) => raw === file || raw.endsWith(`/${file}`))
  return match ? [{ path: match, additions: 0, deletions: 0 }] : undefined
}

function causeToCall(cause: LogCause, index: number) {
  const args = cause.args && typeof cause.args === 'object' ? cause.args as Record<string, unknown> : undefined
  const raw = String(args?.file_path ?? args?.path ?? args?.command ?? args?.query ?? args?.pattern ?? cause.tool)
  const summary = raw.startsWith('/') ? raw.split('/').filter(Boolean).slice(-3).join('/') : raw
  return { id: cause.tool_use_id || `${cause.tool}-${index}`, tool: cause.tool || 'Tool', summary, detail: display(cause.result) }
}

/** The server returns newest-first steps; the transcript is rendered oldest-first. */
export function logToTranscript(log: LogResponse): TranscriptEntry[] {
  const entries: TranscriptEntry[] = []
  for (const [stepIndex, step] of [...log.steps].reverse().entries()) {
    const at = transcriptTime(step.timestamp)
    // The divider introduces the turn it belongs to, so it precedes that turn's messages.
    entries.push({ type: 'step', id: step.hash, at, hash: step.hash.slice(0, 8), tree: '—', turn: `step-${stepIndex + 1}`, tokens: 0, files: step.files.length })
    for (const [messageIndex, item] of step.messages.entries()) {
      const kind = item.type || item.message.role
      const id = `${step.hash}-message-${messageIndex}`
      if (kind === 'reasoning') entries.push({ type: 'reasoning', id, at, duration: 0, lines: item.message.content.split('\n').filter(Boolean) })
      else if (kind === 'user') entries.push({ type: 'user', id, at, content: item.message.content })
      else entries.push({ type: 'assistant', id, at, content: item.message.content })
    }
    if (step.causes.length) entries.push({ type: 'tools', id: `${step.hash}-tools`, at, stepHash: step.hash, calls: step.causes.map(causeToCall), files: step.files.map((path) => ({ path, additions: 0, deletions: 0 })) })
  }
  return entries
}

export function transcriptToEntries(transcript: TranscriptResponse): TranscriptEntry[] {
  const entries: TranscriptEntry[] = []
  // Some older portable conversation blobs repeat the preceding assistant
  // message at the start of the next captured step. Until message ids are part
  // of that legacy shape, suppress exact assistant replays in the combined
  // session view while leaving repeated user prompts untouched.
  let previousAssistant: string | undefined
  for (const step of transcript.steps) {
    const fallbackAt = transcriptTime(step.timestamp)
    const tokens = (step.usage?.input_tokens ?? 0) + (step.usage?.output_tokens ?? 0)
    // The divider introduces the turn it belongs to, so it precedes that turn's events.
    entries.push({ type: 'step', id: step.hash, at: fallbackAt, hash: step.hash.slice(0, 8), tree: step.tree.slice(0, 8), turn: step.turn_id || '—', tokens, files: step.files.length })
    const results = new Map(step.events.filter((event) => event.type === 'tool_result' && event.tool_use_id).map((event) => [event.tool_use_id!, event.output]))
    let sawToolCall = false
    for (const [index, event] of step.events.entries()) {
      const at = transcriptTime(event.timestamp) || fallbackAt
      const id = `${step.hash}-event-${index}`
      if (event.type === 'user' && event.text) {
        entries.push({ type: 'user', id, at, content: event.text })
      }
      else if (event.type === 'assistant' && event.text) {
        const repeatedCarryover = !sawToolCall && event.text === previousAssistant
        previousAssistant = event.text
        if (!repeatedCarryover) entries.push({ type: 'assistant', id, at, content: event.text })
      }
      else if (event.type === 'reasoning' && event.text) {
        entries.push({ type: 'reasoning', id, at, duration: 0, lines: event.text.split('\n').filter(Boolean) })
      }
      else if (event.type === 'tool' || event.type === 'tool_call') {
        sawToolCall = true
        const result = event.output ?? (event.tool_use_id ? results.get(event.tool_use_id) : undefined)
        const call = causeToCall({ tool: event.tool_name || 'Tool', tool_use_id: event.tool_use_id || id, args: event.input, result }, index)
        entries.push({ type: 'tools', id, at, stepHash: step.hash, calls: [call], files: callFiles(event.input, step.files) })
      }
    }
    if (!sawToolCall && step.causes.length) entries.push({ type: 'tools', id: `${step.hash}-tools`, at: fallbackAt, stepHash: step.hash, calls: step.causes.map(causeToCall), files: step.files.map((path) => ({ path, additions: 0, deletions: 0 })) })
  }
  return entries
}
