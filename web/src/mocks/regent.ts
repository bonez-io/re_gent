import type { Conversation, TranscriptEntry } from '../api/types'

export const conversations: Conversation[] = [
  { id: 'codex:01JZQ8MX7D', title: 'Stabilize reminder scheduling', author: 'Shay Livne', agent: 'Codex', model: 'gpt-5.6', branch: 'main', steps: 42, files: 7, relativeTime: '2m', dateGroup: 'Today', status: 'capturing' },
  { id: 'claude:8d3f4a22', title: 'Add relationship memory retrieval', author: 'Arad', agent: 'Claude Code', model: 'Sonnet 4.6', branch: 'feature/memory', steps: 28, files: 11, relativeTime: '18m', dateGroup: 'Today', status: 'complete' },
  { id: 'codex:01JZQ71B9K', title: 'Trace why morning reminders duplicate', author: 'Shay Livne', agent: 'Codex', model: 'gpt-5.6', branch: 'main', steps: 16, files: 4, relativeTime: '46m', dateGroup: 'Today', status: 'complete' },
  { id: 'opencode:local-482', title: 'Prototype calendar provider adapter', author: 'Amir', agent: 'OpenCode', model: 'Kimi K2.5', branch: 'feat/calendar', steps: 9, files: 6, relativeTime: '1h', dateGroup: 'Today', status: 'complete' },
  { id: 'claude:31d50bc9', title: 'Review prompt injection boundaries', author: 'Arad', agent: 'Claude Code', model: 'Opus 4.6', branch: 'security/prompt-boundary', steps: 31, files: 8, relativeTime: 'Yesterday', dateGroup: 'Yesterday', status: 'complete' },
  { id: 'legacy:7f18', title: 'Initial onboarding prompt', branch: 'main', steps: 6, files: 2, relativeTime: 'Yesterday', dateGroup: 'Yesterday', status: 'legacy' },
  { id: 'codex:01JYTZ03GN', title: 'Move preferences into structured context', author: 'Shay Livne', agent: 'Codex', model: 'gpt-5.5', branch: 'main', steps: 23, files: 9, relativeTime: 'Aug 14', dateGroup: 'Earlier', status: 'complete' },
]

export const transcript: TranscriptEntry[] = [
  { type: 'user', id: 'm1', at: '13:04:12', content: 'The reminder parser keeps moving “tomorrow morning” into UTC before it knows the user timezone. Trace the full path, fix it, and keep the change narrow.' },
  { type: 'reasoning', id: 'm2', at: '13:04:18', duration: 18, lines: ['Locate the natural-language parser and every timezone conversion call.', 'Confirm whether the stored reminder contract expects UTC or local wall time.', 'Find the smallest seam where timezone context is still available.'] },
  { type: 'assistant', id: 'm3', at: '13:04:22', content: 'I’ll trace parsing through persistence first, then patch the earliest incorrect conversion and add a regression at that boundary.' },
  { type: 'tools', id: 'm4', at: '13:04:25', calls: [
    { id: 't1', tool: 'Read', summary: 'src/reminders/parser.ts', detail: ['Read 184 lines', 'parseNaturalDate converts with system timezone at line 61'] },
    { id: 't2', tool: 'Search', summary: 'normalizeTimezone(', detail: ['src/reminders/parser.ts:61', 'src/reminders/service.ts:114', 'src/reminders/parser.test.ts:203'] },
  ] },
  { type: 'assistant', id: 'm5', at: '13:04:39', content: 'The stored contract is UTC, but conversion belongs in the service after the profile timezone is attached. I found one premature conversion in the parser.' },
  { type: 'tools', id: 'm6', at: '13:04:48', calls: [
    { id: 't3', tool: 'Edit', summary: 'src/reminders/parser.ts', detail: ['− normalizeTimezone(parsed, systemTimezone)', '+ return { parsed, sourceTimezone: null }'] },
    { id: 't4', tool: 'Edit', summary: 'src/reminders/service.ts', detail: ['+ normalizeTimezone(parsed, profile.timezone)', '+ retain source phrase for provenance'] },
    { id: 't5', tool: 'Bash', summary: 'pnpm test parser reminder-service', detail: ['✓ parser: 18 passed', '✓ reminder-service: 24 passed', 'Completed in 1.8s'] },
  ], files: [{ path: 'src/reminders/parser.ts', additions: 7, deletions: 4 }, { path: 'src/reminders/service.ts', additions: 11, deletions: 3 }, { path: 'src/reminders/parser.test.ts', additions: 29, deletions: 0 }] },
  { type: 'code', id: 'm7', at: '13:05:02', filename: 'src/reminders/parser.ts', language: 'TypeScript', code: 'export function parseReminder(input: string): ParsedReminder {\n  const parsed = parseNaturalDate(input)\n  return { parsed, sourcePhrase: input }\n}' },
  { type: 'assistant', id: 'm8', at: '13:05:07', content: 'The parser now preserves local intent. UTC conversion happens once, in the service, with the user profile timezone. The focused suites pass.' },
  { type: 'step', id: 's1', at: '13:05:09', hash: '7ac3ef1', tree: 'e4b8a20', turn: 'turn-184', tokens: 1842, files: 3 },
  { type: 'user', id: 'm9', at: '13:08:41', content: 'Good. Add one case for a DST boundary and run the complete reminder suite.' },
  { type: 'reasoning', id: 'm10', at: '13:08:45', duration: 9, lines: ['Use a timezone with a known spring-forward boundary.', 'Assert the intended wall time rather than an implementation-specific offset.', 'Run the full reminder package after the focused case.'] },
  { type: 'tools', id: 'm11', at: '13:08:53', calls: [
    { id: 't6', tool: 'Edit', summary: 'src/reminders/parser.test.ts', detail: ['+ America/New_York spring-forward case', '+ preserves 09:00 local intent'] },
    { id: 't7', tool: 'Bash', summary: 'pnpm test reminders', detail: ['✓ 67 tests passed', 'Completed in 3.4s'] },
  ], files: [{ path: 'src/reminders/parser.test.ts', additions: 17, deletions: 0 }] },
  { type: 'assistant', id: 'm12', at: '13:09:08', content: 'Added the DST regression using America/New_York and verified all 67 reminder tests.' },
  { type: 'step', id: 's2', at: '13:09:10', hash: 'bd91c42', tree: '7fe206a', turn: 'turn-185', tokens: 724, files: 1 },
]

export const blameLines = [
  { number: 58, hash: '41ac200', author: 'Arad', code: 'export function parseReminder(input: string): ParsedReminder {' },
  { number: 59, hash: '7ac3ef1', author: 'Shay', code: '  const parsed = parseNaturalDate(input)' },
  { number: 60, hash: '7ac3ef1', author: 'Shay', code: '  return { parsed, sourcePhrase: input }' },
  { number: 61, hash: '41ac200', author: 'Arad', code: '}' },
]
