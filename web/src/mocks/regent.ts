import type {
  BlameResponse,
  Conversation,
  FilesResponse,
  LogResponse,
  SessionSummary,
  SessionsResponse,
  StatusResponse,
  TranscriptEntry,
  TranscriptResponse,
} from '../api/types'

export const demoRepoId = 'demo-agent-workspace'

const now = Date.now()
const minutesAgo = (minutes: number) => new Date(now - minutes * 60_000).toISOString()
const hash = (seed: string) => seed.padEnd(64, seed).slice(0, 64)

export const mockSessions: SessionSummary[] = [
  { session_id: 'codex:01JZQ8MX7D', title: 'Stabilize reminder scheduling', author: { name: 'Shay Livne', email: 'shay@regent.dev' }, agent_id: 'Codex', step_count: 42, last_activity: minutesAgo(2) },
  { session_id: 'claude:shay-handoff', title: 'Recheck DST handoff notes', author: { name: 'Shay Livne', email: 'shay@regent.dev' }, agent_id: 'Claude Code', step_count: 25, last_activity: minutesAgo(38) },
  { session_id: 'claude:8d3f4a22', title: 'Add relationship memory retrieval', author: { name: 'Arad', email: 'arad@regent.dev' }, agent_id: 'Claude Code', step_count: 28, last_activity: minutesAgo(18) },
  { session_id: 'codex:01JZQ71B9K', title: 'Trace why morning reminders duplicate', author: { name: 'Shay Livne', email: 'shay@regent.dev' }, agent_id: 'Codex', step_count: 16, last_activity: minutesAgo(46) },
  { session_id: 'opencode:local-482', title: 'Prototype calendar provider adapter', author: { name: 'Amir', email: 'amir@regent.dev' }, agent_id: 'OpenCode', step_count: 9, last_activity: minutesAgo(74) },
  { session_id: 'claude:31d50bc9', title: 'Review prompt injection boundaries', author: { name: 'Arad', email: 'arad@regent.dev' }, agent_id: 'Claude Code', step_count: 31, last_activity: minutesAgo(26 * 60) },
]

export const mockSessionsResponse: SessionsResponse = {
  total_sessions: mockSessions.length,
  sessions: mockSessions,
}

const parserStep = hash('7ac3ef1')
const serviceStep = hash('bd91c42')
const earlierStep = hash('41ac200')

export const mockLogResponse: LogResponse = {
  session_id: mockSessions[0].session_id,
  steps: [
    {
      hash: serviceStep,
      parent: parserStep,
      timestamp: minutesAgo(1),
      origin: 'codex',
      tool: 'Bash',
      tool_use_id: 't7',
      args: { command: 'pnpm test reminders' },
      result: { status: 'passed', output: '67 tests passed' },
      files: ['src/reminders/parser.test.ts'],
      causes: [
        { tool: 'Edit', tool_use_id: 't6', args: { file_path: 'src/reminders/parser.test.ts' }, result: '+ America/New_York spring-forward case' },
        { tool: 'Bash', tool_use_id: 't7', args: { command: 'pnpm test reminders' }, result: '✓ 67 tests passed\nCompleted in 3.4s' },
      ],
      messages: [
        { type: 'user', message: { role: 'user', content: 'Good. Add one case for a DST boundary and run the complete reminder suite.' } },
        { type: 'assistant', message: { role: 'assistant', content: 'Added the DST regression using America/New_York and verified all 67 reminder tests.' } },
      ],
    },
    {
      hash: parserStep,
      parent: earlierStep,
      timestamp: minutesAgo(6),
      origin: 'codex',
      tool: 'Edit',
      tool_use_id: 't3',
      args: { file_path: 'src/reminders/parser.ts' },
      result: { status: 'patched' },
      files: ['src/reminders/parser.ts', 'src/reminders/service.ts', 'src/reminders/parser.test.ts'],
      causes: [
        { tool: 'Read', tool_use_id: 't1', args: { file_path: 'src/reminders/parser.ts' }, result: 'Read 184 lines' },
        { tool: 'Search', tool_use_id: 't2', args: { query: 'normalizeTimezone(' }, result: 'src/reminders/parser.ts:61\nsrc/reminders/service.ts:114' },
        { tool: 'Edit', tool_use_id: 't3', args: { file_path: 'src/reminders/parser.ts' }, result: 'Moved timezone normalization out of parser' },
        { tool: 'Edit', tool_use_id: 't4', args: { file_path: 'src/reminders/service.ts' }, result: 'Normalize with profile timezone at persistence boundary' },
        { tool: 'Bash', tool_use_id: 't5', args: { command: 'pnpm test parser reminder-service' }, result: '✓ parser: 18 passed\n✓ reminder-service: 24 passed' },
      ],
      messages: [
        { type: 'user', message: { role: 'user', content: 'The reminder parser keeps moving “tomorrow morning” into UTC before it knows the user timezone. Trace the full path, fix it, and keep the change narrow.' } },
        { type: 'reasoning', message: { role: 'assistant', content: 'Locate the parser and every timezone conversion call.\nConfirm whether stored reminders expect UTC or local wall time.\nPatch the earliest place where timezone context is still available.' } },
        { type: 'assistant', message: { role: 'assistant', content: 'The parser now preserves local intent. UTC conversion happens once, in the service, with the user profile timezone. The focused suites pass.' } },
      ],
    },
  ],
}

export const mockTranscriptResponse: TranscriptResponse = {
  session: mockSessions[0],
  steps: [
    {
      hash: parserStep,
      parent: earlierStep,
      tree: hash('e4b8a20'),
      timestamp: minutesAgo(6),
      origin: 'codex',
      session_id: mockSessions[0].session_id,
      turn_id: 'turn-184',
      agent_id: 'Codex',
      author: mockSessions[0].author,
      usage: { input_tokens: 1240, output_tokens: 602, cache_read_tokens: 8600 },
      effects: [],
      files: ['src/reminders/parser.ts', 'src/reminders/service.ts', 'src/reminders/parser.test.ts'],
      causes: mockLogResponse.steps[1].causes,
      events: [
        { type: 'user', timestamp: minutesAgo(7), text: 'The reminder parser keeps moving “tomorrow morning” into UTC before it knows the user timezone. Trace the full path, fix it, and keep the change narrow.' },
        { type: 'reasoning', timestamp: minutesAgo(7), text: 'Locate the natural-language parser and every timezone conversion call.\nConfirm whether the stored reminder contract expects UTC or local wall time.\nFind the smallest boundary where timezone context is still available.' },
        { type: 'assistant', timestamp: minutesAgo(6), text: 'I’ll trace parsing through persistence first, then patch the earliest incorrect conversion and add a regression at that boundary.' },
        { type: 'tool_call', timestamp: minutesAgo(6), tool_name: 'Read', tool_use_id: 't1', input: { file_path: 'src/reminders/parser.ts' }, output: 'Read 184 lines\nparseNaturalDate converts with system timezone at line 61' },
        { type: 'tool_call', timestamp: minutesAgo(6), tool_name: 'Search', tool_use_id: 't2', input: { query: 'normalizeTimezone(' }, output: 'src/reminders/parser.ts:61\nsrc/reminders/service.ts:114\nsrc/reminders/parser.test.ts:203' },
        { type: 'assistant', timestamp: minutesAgo(5), text: 'The stored contract is UTC, but conversion belongs in the service after the profile timezone is attached. I found one premature conversion in the parser.' },
        { type: 'tool_call', timestamp: minutesAgo(5), tool_name: 'Edit', tool_use_id: 't3', input: { file_path: 'src/reminders/parser.ts' }, output: '− normalizeTimezone(parsed, systemTimezone)\n+ return { parsed, sourceTimezone: null }' },
        { type: 'tool_call', timestamp: minutesAgo(4), tool_name: 'Edit', tool_use_id: 't4', input: { file_path: 'src/reminders/service.ts' }, output: '+ normalizeTimezone(parsed, profile.timezone)\n+ retain source phrase for provenance' },
        { type: 'tool_call', timestamp: minutesAgo(4), tool_name: 'Bash', tool_use_id: 't5', input: { command: 'pnpm test parser reminder-service' }, output: '✓ parser: 18 passed\n✓ reminder-service: 24 passed\nCompleted in 1.8s' },
        { type: 'assistant', timestamp: minutesAgo(3), text: 'The parser now preserves local intent. UTC conversion happens once, in the service, with the user profile timezone. The focused suites pass.' },
      ],
    },
    {
      hash: serviceStep,
      parent: parserStep,
      tree: hash('7fe206a'),
      timestamp: minutesAgo(1),
      origin: 'codex',
      session_id: mockSessions[0].session_id,
      turn_id: 'turn-185',
      agent_id: 'Codex',
      author: mockSessions[0].author,
      usage: { input_tokens: 512, output_tokens: 212, cache_read_tokens: 11200 },
      effects: [],
      files: ['src/reminders/parser.test.ts'],
      causes: mockLogResponse.steps[0].causes,
      events: [
        { type: 'user', timestamp: minutesAgo(2), text: 'Good. Add one case for a DST boundary and run the complete reminder suite.' },
        { type: 'reasoning', timestamp: minutesAgo(2), text: 'Use a timezone with a known spring-forward boundary.\nAssert the intended wall time rather than an implementation-specific offset.\nRun the full reminder package after the focused case.' },
        { type: 'tool_call', timestamp: minutesAgo(2), tool_name: 'Edit', tool_use_id: 't6', input: { file_path: 'src/reminders/parser.test.ts' }, output: '+ America/New_York spring-forward case\n+ preserves 09:00 local intent' },
        { type: 'tool_call', timestamp: minutesAgo(1), tool_name: 'Bash', tool_use_id: 't7', input: { command: 'pnpm test reminders' }, output: '✓ 67 tests passed\nCompleted in 3.4s' },
        { type: 'assistant', timestamp: minutesAgo(1), text: 'Added the DST regression using America/New_York and verified all 67 reminder tests.' },
      ],
    },
  ],
}

export const mockFilesResponse: FilesResponse = {
  step_hash: serviceStep,
  tree_hash: hash('7fe206a'),
  total_files: 7,
  files: [
    { path: 'src/reminders/parser.ts', blob_hash: hash('parser'), blame_hash: hash('parser-blame'), size: 18420 },
    { path: 'src/reminders/service.ts', blob_hash: hash('service'), blame_hash: hash('service-blame'), size: 9210 },
    { path: 'src/reminders/parser.test.ts', blob_hash: hash('parser-test'), blame_hash: hash('parser-test-blame'), size: 14780 },
    { path: 'src/reminders/timezone.ts', blob_hash: hash('timezone'), blame_hash: hash('timezone-blame'), size: 4380 },
    { path: 'src/reminders/types.ts', blob_hash: hash('types'), blame_hash: hash('types-blame'), size: 1180 },
    { path: 'docs/reminders.md', blob_hash: hash('docs'), blame_hash: hash('docs-blame'), size: 3260 },
    { path: 'package.json', blob_hash: hash('package'), blame_hash: hash('package-blame'), size: 2240 },
  ],
}

export const mockBlameResponse: BlameResponse = {
  step_hash: serviceStep,
  path: 'src/reminders/parser.ts',
  blob_hash: hash('parser'),
  lines: [
    { number: 58, step_hash: earlierStep, origin: 'claude', content: 'export function parseReminder(input: string): ParsedReminder {' },
    { number: 59, step_hash: parserStep, origin: 'codex', content: '  const parsed = parseNaturalDate(input)' },
    { number: 60, step_hash: parserStep, origin: 'codex', content: '  return { parsed, sourcePhrase: input }' },
    { number: 61, step_hash: earlierStep, origin: 'claude', content: '}' },
    { number: 62, step_hash: serviceStep, origin: 'codex', content: '' },
    { number: 63, step_hash: serviceStep, origin: 'codex', content: 'export function normalizeForStorage(parsed: ParsedReminder, profile: UserProfile) {' },
    { number: 64, step_hash: serviceStep, origin: 'codex', content: '  return normalizeTimezone(parsed, profile.timezone)' },
    { number: 65, step_hash: serviceStep, origin: 'codex', content: '}' },
  ],
}

export const mockStatusResponse: StatusResponse = {
  status: 'ok',
  service: { name: 're_gent demo server', api_version: 'local-ui-mock', server_url: 'http://127.0.0.1:7654' },
  repository: { id: demoRepoId, object_count: 128, ref_count: 5, session_count: mockSessions.length, last_activity: mockSessions[0].last_activity },
}

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
