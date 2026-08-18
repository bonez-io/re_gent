import { CodeBlock } from './components/CodeBlock'
import { ProjectSidebar } from './components/ProjectSidebar'
import { SessionRow } from './components/SessionRow'
import { ThinkingReasoning } from './components/ThinkingReasoning'
import { ToolCallGroup } from './components/ToolCallGroup'

const calls = [
  { id: 'read', tool: 'Read', summary: 'src/reminders/parser.ts', detail: ['Read 184 lines', 'Located parseReminder at line 42'] },
  { id: 'edit', tool: 'Edit', summary: 'src/reminders/parser.ts', detail: ['+ normalize relative dates before timezone conversion', '+ preserve the source timezone in metadata'] },
  { id: 'test', tool: 'Bash', summary: 'pnpm test parser', detail: ['✓ 18 tests passed', 'Completed in 1.2s'] },
]

function App() {
  return <div className="min-h-screen bg-page p-4 text-ink sm:p-6">
    <div className="mx-auto grid max-w-[1240px] grid-cols-[240px_minmax(0,1fr)] gap-3 max-md:grid-cols-1">
      <div className="max-md:hidden"><ProjectSidebar /></div>
      <main className="overflow-hidden rounded-window bg-surface shadow-card">
        <header className="flex min-h-13 items-center justify-between border-b border-line px-4">
          <div><h1 className="m-0 text-[14px] font-semibold tracking-[-0.01em]">girlfriend-assistant</h1><p className="m-0 mt-0.5 text-[11.5px] text-ink-3">Agent activity and provenance</p></div>
          <div className="flex items-center gap-2 text-[11.5px] text-ink-3"><span className="size-1.5 rounded-full bg-green" />Connected</div>
        </header>
        <div className="grid grid-cols-[minmax(300px,0.85fr)_minmax(420px,1.25fr)] max-lg:grid-cols-1">
          <section className="border-r border-line max-lg:border-r-0 max-lg:border-b" aria-label="Sessions">
            <div className="flex h-11 items-center justify-between border-b border-line px-3"><h2 className="m-0 text-[13px] font-semibold">Sessions</h2><button className="rounded-control bg-field px-2 py-1 text-[11.5px] text-ink-2 shadow-hairline">main ▾</button></div>
            <div className="px-3 pb-1 pt-3 text-[10.5px] font-medium uppercase tracking-[0.08em] text-ink-3">Today</div>
            <SessionRow title="Refine reminder scheduling" author="Shay Livne" agent="Codex" model="gpt-5.6" steps={42} relativeTime="2m" selected />
            <SessionRow title="Add relationship context" author="Arad" agent="Claude" model="Sonnet" steps={28} relativeTime="18m" />
            <SessionRow title="Document onboarding prompt" author="Amir" agent="Codex" model="gpt-5.6" steps={9} relativeTime="1h" />
          </section>
          <section className="min-w-0 p-5" aria-label="Step context">
            <div className="mb-5 flex items-start justify-between gap-3"><div><span className="font-mono text-[11px] text-accent-ink">7ac3ef1</span><h2 className="m-0 mt-1 text-[16px] font-semibold tracking-[-0.015em]">Refine reminder scheduling</h2><p className="m-0 mt-1 text-[12px] text-ink-3">Shay Livne · Codex · 2 minutes ago</p></div><span className="rounded-[6px] bg-green-tint px-2 py-1 text-[11px] font-medium text-green">captured</span></div>
            <div className="space-y-5">
              <div><div className="mb-1.5 text-[11px] font-medium uppercase tracking-[0.08em] text-ink-3">Prompt</div><p className="m-0 max-w-2xl text-[13px] leading-5.5 text-ink">Make reminders understand natural dates without losing the user's timezone.</p></div>
              <ThinkingReasoning durationSeconds={12} lines={['Traced reminder parsing into timezone normalization.', 'The parser converts too early, so the original timezone context is lost.', 'The smallest safe change is to normalize only after the natural date is resolved.']} />
              <div><div className="mb-1.5 text-[11px] font-medium uppercase tracking-[0.08em] text-ink-3">Response</div><p className="m-0 max-w-2xl text-[13px] leading-5.5 text-ink-2">I separated natural-date parsing from timezone conversion and added focused regression coverage.</p></div>
              <ToolCallGroup calls={calls} files={[{ path: 'parser.ts', additions: 18, deletions: 6 }, { path: 'parser.test.ts', additions: 34, deletions: 0 }]} />
              <CodeBlock filename="parser.ts" language="TypeScript" code={'export function parseReminder(input: string, timezone: string) {\n  const parsed = parseNaturalDate(input)\n  return normalizeTimezone(parsed, timezone)\n}'} />
            </div>
          </section>
        </div>
      </main>
    </div>
  </div>
}

export default App
