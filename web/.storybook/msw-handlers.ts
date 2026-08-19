import { http, HttpResponse } from 'msw'

const sessions = [
  { session_id: 'codex:01JZQ8MX7D', agent_id: 'Codex', step_count: 12, last_activity: new Date().toISOString(), title: 'Restyle sessions transcript feed', author: { name: 'Shay Livne' } },
  { session_id: 'claude:8d3f4a22', agent_id: 'Claude Code', step_count: 8, last_activity: new Date(Date.now() - 3_600_000).toISOString(), title: 'Sketch provenance cards', author: { name: 'Arad' } },
]

const now = Date.now()
const minutesAgo = (minutes: number) => new Date(now - minutes * 60_000).toISOString()
const padded = (value: string) => value.padEnd(64, '0')

export const mswHandlers = [
  http.get('/repos', () => HttpResponse.json({ repos: ['girlfriend-assistant'] })),
  http.post('/repos', async ({ request }) => {
    const body = await request.json() as { repo_id: string }
    return HttpResponse.json({ repo_id: body.repo_id, created: true }, { status: 201 })
  }),
  http.get('/girlfriend-assistant/api/sessions', () => HttpResponse.json({ total_sessions: sessions.length, sessions })),
  http.get('/girlfriend-assistant/api/transcript', () => HttpResponse.json({
    session: sessions[0],
    steps: [
      {
        hash: padded('7ac3ef1'),
        parent: '',
        tree: padded('e4b8a20'),
        timestamp: minutesAgo(2),
        origin: 'codex',
        session_id: sessions[0].session_id,
        turn_id: 'turn-184',
        agent_id: 'Codex',
        files: [
          'web/src/App.tsx',
          'web/src/components/ConversationTranscript.tsx',
          'web/src/components/ToolCallGroup.tsx',
        ],
        causes: [
          { tool: 'Read', tool_use_id: 't1', args: { file_path: 'web/src/App.tsx' }, result: 'Read sessions layout' },
          { tool: 'Edit', tool_use_id: 't2', args: { file_path: 'web/src/components/ConversationTranscript.tsx' }, result: 'Converted transcript to feed rhythm' },
          { tool: 'Bash', tool_use_id: 't3', args: { command: 'node node_modules/typescript/bin/tsc -b' }, result: 'passed' },
        ],
        events: [
          { type: 'user', timestamp: minutesAgo(7), text: 'Make the sessions screen feel closer to the product website, but keep the transcript useful for debugging.' },
          { type: 'assistant', timestamp: minutesAgo(6), text: 'I will make the session transcript read more like a real chat while preserving timestamps, tool provenance, reasoning, file details, and step markers.' },
          { type: 'tool_call', timestamp: minutesAgo(6), tool_name: 'Read', tool_use_id: 't1', input: { file_path: 'web/src/App.tsx' }, output: 'Read sessions layout and selected-session behavior' },
          { type: 'tool_call', timestamp: minutesAgo(6), tool_name: 'Read', tool_use_id: 't2', input: { file_path: 'web/src/components/ConversationTranscript.tsx' }, output: 'Read transcript rendering and entry type handling' },
          { type: 'assistant', timestamp: minutesAgo(5), text: 'I am replacing the compressed log rows with a clearer feed. User turns stay on the right in compact bubbles, and agent work stays on the left as prose and action checkpoints.' },
          { type: 'tool_call', timestamp: minutesAgo(5), tool_name: 'Edit', tool_use_id: 't3', input: { file_path: 'web/src/App.tsx' }, output: 'Added the Viewing as selector above Sessions\nFiltered sessions by selected team member' },
          { type: 'assistant', timestamp: minutesAgo(4), text: 'The viewer selector is in. Now I am reshaping the conversation itself so action labels sit in the feed instead of reading like table rows.' },
          { type: 'tool_call', timestamp: minutesAgo(4), tool_name: 'Edit', tool_use_id: 't4', input: { file_path: 'web/src/components/ConversationTranscript.tsx' }, output: 'Removed per-action separators\nGave transcript entries a spacious feed rhythm' },
          { type: 'tool_call', timestamp: minutesAgo(4), tool_name: 'Edit', tool_use_id: 't5', input: { file_path: 'web/src/components/ToolCallGroup.tsx' }, output: 'Mapped tool groups to labels like Read files and Edited a file\nKept details visible under each action' },
          { type: 'reasoning', timestamp: minutesAgo(3), text: 'Keep the dark re_gent surface.\nBorrow the screenshot rhythm: large prose, quiet action labels, no horizontal separators.\nKeep provenance details visible without turning the transcript back into a grid.' },
          { type: 'assistant', timestamp: minutesAgo(3), text: 'I caught the last table-like pieces in the action block. The mock data now uses realistic design-iteration turns so the story shows the intended behavior immediately.' },
          { type: 'tool_call', timestamp: minutesAgo(2), tool_name: 'Bash', tool_use_id: 't6', input: { command: 'node node_modules/typescript/bin/tsc -b' }, output: 'TypeScript check passed' },
          { type: 'tool_call', timestamp: minutesAgo(2), tool_name: 'Bash', tool_use_id: 't7', input: { command: 'node node_modules/vite/bin/vite.js build' }, output: 'Production build completed' },
          { type: 'assistant', timestamp: minutesAgo(1), text: 'Core checks pass. Storybook is showing the sessions story with the chat-style transcript and no separating lines between actions.' },
        ],
      },
    ],
  })),
  http.get('/girlfriend-assistant/api/log', () => HttpResponse.json({ session_id: sessions[0].session_id, steps: [] })),
  http.get('/girlfriend-assistant/api/status', () => HttpResponse.json({ status: 'ok', service: { name: 're_gent', api_version: 'v1' }, repository: { id: 'girlfriend-assistant', object_count: 1284, ref_count: 7, session_count: 2, last_activity: new Date().toISOString() } })),
  http.get('/girlfriend-assistant/api/files', () => HttpResponse.json({ step_hash: padded('7ac3ef1'), tree_hash: padded('e4b8a20'), total_files: 1, files: [{ path: 'web/src/components/ConversationTranscript.tsx', blob_hash: padded('b1'), mode: 420, size: 1840 }] })),
  http.get('/girlfriend-assistant/api/blame', () => HttpResponse.json({ step_hash: padded('7ac3ef1'), path: 'web/src/components/ConversationTranscript.tsx', blob_hash: padded('b1'), lines: [{ number: 1, content: 'export function ConversationTranscript() {', step_hash: padded('7ac3ef1'), origin: 'codex' }] })),
]
