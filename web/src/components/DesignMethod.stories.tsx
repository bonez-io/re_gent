import type { Meta, StoryObj } from '@storybook/react-vite'
import { CodeBlock } from './CodeBlock'
import { SessionRow } from './SessionRow'
import { StepMarker } from './StepMarker'
import { ToolCallGroup } from './ToolCallGroup'

function Swatch({ label, value, className }: { label: string; value: string; className: string }) {
  return <div className="grid grid-cols-[44px_minmax(0,1fr)] items-center gap-2">
    <span className={`h-8 border border-line ${className}`} />
    <span className="min-w-0"><span className="block text-[11.5px] font-medium text-ink">{label}</span><span className="block font-mono text-[10px] text-ink-3">{value}</span></span>
  </div>
}

function DesignMethod() {
  return <main className="min-h-screen bg-page text-ink">
    <section className="mx-auto grid max-w-[1180px] gap-8 px-6 py-8">
      <header className="border-b border-line pb-6">
        <span className="regent-kicker">Design method</span>
        <div className="mt-5 grid gap-5 md:grid-cols-[minmax(0,520px)_minmax(360px,1fr)] md:items-end">
          <div>
            <h1 className="m-0 text-[54px] font-semibold leading-[1.04] max-sm:text-[38px]">Version-control surfaces for agent activity.</h1>
            <p className="m-0 mt-4 max-w-[420px] text-[15px] leading-6 text-ink-3">The interface borrows the website's stark landing-page grammar: near-black canvas, white type, thin ruled boxes, hatch bands, terminal evidence, compact metadata, and provenance rails.</p>
          </div>
          <div className="regent-terminal rounded-[4px] p-4 font-mono text-[11.5px] leading-6">
            <p className="m-0 text-terminal-muted">$ rgt log --graph --all</p>
            <p className="m-0"><span className="text-green">*</span> a7f3e891 15:15 <span className="text-green">[Session A]</span> Edit</p>
            <p className="m-0 pl-4 text-terminal-muted">Human: "add rate limiting to the API"</p>
            <p className="m-0"><span className="text-yellow">*</span> 5a2e8d4c 15:00 <span className="text-yellow">[Session B]</span> Edit</p>
            <p className="m-0 pl-4 text-terminal-muted">Human: "add JWT token validation"</p>
            <p className="m-0"><span className="text-red">*</span> c2d8f3a4 11:30 <span className="text-red">[Session C]</span> Write</p>
          </div>
        </div>
      </header>

      <section className="grid gap-4 md:grid-cols-[260px_minmax(0,1fr)]">
        <div>
          <h2 className="m-0 text-[18px] font-semibold">Tokens</h2>
          <p className="m-0 mt-1 text-[12px] leading-5 text-ink-3">Mostly monochrome. Color is reserved for git-like state: success, failure, warning, and terminal graph traces.</p>
        </div>
        <div className="regent-hairline-panel grid gap-4 rounded-[4px] p-4 sm:grid-cols-2 lg:grid-cols-4">
          <Swatch label="Page" value="#080808" className="bg-page" />
          <Swatch label="Canvas" value="#0f0f0f" className="bg-canvas" />
          <Swatch label="Line" value="#303030" className="bg-line" />
          <Swatch label="Ink" value="#f4f4f4" className="bg-ink" />
          <Swatch label="Inset" value="#101010" className="bg-inset" />
          <Swatch label="Green" value="#6ee18f" className="bg-green" />
          <Swatch label="Yellow" value="#ffe100" className="bg-yellow" />
          <Swatch label="Terminal" value="#090909" className="bg-terminal" />
        </div>
      </section>

      <section className="grid gap-4 md:grid-cols-[260px_minmax(0,1fr)]">
        <div>
          <h2 className="m-0 text-[18px] font-semibold">Elements</h2>
          <p className="m-0 mt-1 text-[12px] leading-5 text-ink-3">Controls stay square, dense, and bordered. Hashes and paths use mono type so the product feels native to source history.</p>
        </div>
        <div className="grid gap-4 lg:grid-cols-2">
          <div className="regent-hairline-panel rounded-[4px] p-4">
            <div className="mb-3 flex items-center gap-2">
              <button className="h-9 rounded-[3px] border border-line bg-canvas px-3 text-[12px] font-semibold text-ink shadow-hairline">View on GitHub</button>
              <button className="h-9 rounded-[3px] border border-line bg-field px-3 text-[12px] font-semibold text-ink shadow-hairline">Copy hash</button>
              <span className="regent-kicker">Public alpha</span>
            </div>
            <StepMarker hash="7ac3ef1" tree="e4b8a20" turn="turn-184" tokens={1842} files={3} at="13:05:09" />
          </div>
          <div className="overflow-hidden rounded-[4px] border border-line">
            <SessionRow id="codex:01JZQ8MX7D" title="Stabilize reminder scheduling" author="Shay Livne" agent="Codex" model="gpt-5.6" branch="main" steps={42} files={7} relativeTime="2m" dateGroup="Today" status="capturing" selected />
            <SessionRow id="claude:8d3f4a22" title="Add relationship memory retrieval" author="Arad" agent="Claude Code" model="Sonnet 4.6" branch="feature/memory" steps={28} files={11} relativeTime="18m" dateGroup="Today" status="complete" />
          </div>
        </div>
      </section>

      <section className="grid gap-4 md:grid-cols-[260px_minmax(0,1fr)]">
        <div>
          <h2 className="m-0 text-[18px] font-semibold">Evidence</h2>
          <p className="m-0 mt-1 text-[12px] leading-5 text-ink-3">The UI should always show the artifact behind a claim: command, file, step, prompt, or result.</p>
        </div>
        <div className="grid gap-4 lg:grid-cols-2">
          <CodeBlock filename="src/reminders/parser.ts" language="TypeScript" code={'export function parseReminder(input: string) {\n  const parsed = parseNaturalDate(input)\n  return { parsed, sourcePhrase: input }\n}'} />
          <div className="regent-hairline-panel rounded-[4px] p-4">
            <ToolCallGroup defaultOpen calls={[
              { id: 'read', tool: 'Read', summary: 'src/reminders/parser.ts', detail: ['Read 184 lines', 'parseNaturalDate converts with system timezone at line 61'] },
              { id: 'edit', tool: 'Edit', summary: 'src/reminders/service.ts', detail: ['+ normalizeTimezone(parsed, profile.timezone)', '+ retain source phrase for provenance'] },
            ]} files={[{ path: 'src/reminders/parser.ts', additions: 7, deletions: 4 }]} />
          </div>
        </div>
      </section>
    </section>
  </main>
}

const meta = { component: DesignMethod, title: 'Design/Regent Method', parameters: { layout: 'fullscreen' }, tags: ['ai-generated'] } satisfies Meta<typeof DesignMethod>
export default meta
type Story = StoryObj<typeof meta>

export const Method: Story = {}
