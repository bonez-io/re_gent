import { http, HttpResponse } from 'msw'

const sessions = [
  { session_id: 'codex:01JZQ8MX7D', agent_id: 'Codex', step_count: 2, last_activity: new Date().toISOString(), title: 'Stabilize reminder scheduling', author: { name: 'Shay Livne' } },
  { session_id: 'claude:8d3f4a22', agent_id: 'Claude Code', step_count: 1, last_activity: new Date(Date.now() - 3_600_000).toISOString(), title: 'Add relationship memory retrieval', author: { name: 'Arad' } },
]

export const mswHandlers = [
  http.get('/repos', () => HttpResponse.json({ repos: ['girlfriend-assistant'] })),
  http.post('/repos', async ({ request }) => { const body = await request.json() as { repo_id: string }; return HttpResponse.json({ repo_id: body.repo_id, created: true }, { status: 201 }) }),
  http.get('/girlfriend-assistant/api/sessions', () => HttpResponse.json({ total_sessions: sessions.length, sessions })),
  http.get('/girlfriend-assistant/api/transcript', () => HttpResponse.json({ session: sessions[0], steps: [{ hash: '7ac3ef1'.padEnd(64, '0'), parent: '', tree: 'e4b8a20'.padEnd(64, '0'), timestamp: new Date().toISOString(), origin: 'codex', session_id: sessions[0].session_id, turn_id: 'turn-184', agent_id: 'Codex', files: ['src/reminders/parser.ts'], causes: [{ tool: 'Edit', tool_use_id: 't1', args: { file_path: '/Users/shay/Projects/girlfriend-assistant/src/reminders/parser.ts' }, result: 'updated' }], events: [{ type: 'user', text: 'Trace the reminder timezone bug and keep the change narrow.' }, { type: 'reasoning', text: 'Locate the first conversion boundary.\nConfirm the stored reminder contract.' }, { type: 'assistant', text: 'I found the conversion before profile context is attached.' }] }] })),
  http.get('/girlfriend-assistant/api/log', () => HttpResponse.json({ session_id: sessions[0].session_id, steps: [] })),
  http.get('/girlfriend-assistant/api/status', () => HttpResponse.json({ status: 'ok', service: { name: 're_gent', api_version: 'v1' }, repository: { id: 'girlfriend-assistant', object_count: 1284, ref_count: 7, session_count: 2, last_activity: new Date().toISOString() } })),
  http.get('/girlfriend-assistant/api/files', () => HttpResponse.json({ step_hash: '7ac3ef1'.padEnd(64, '0'), tree_hash: 'e4b8a20'.padEnd(64, '0'), total_files: 1, files: [{ path: 'src/reminders/parser.ts', blob_hash: 'b1'.padEnd(64, '0'), mode: 420, size: 1840 }] })),
  http.get('/girlfriend-assistant/api/blame', () => HttpResponse.json({ step_hash: '7ac3ef1'.padEnd(64, '0'), path: 'src/reminders/parser.ts', blob_hash: 'b1'.padEnd(64, '0'), lines: [{ number: 1, content: 'export function parseReminder(input: string) {', step_hash: '7ac3ef1'.padEnd(64, '0'), origin: 'codex' }] })),
]
