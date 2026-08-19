import type { Skill } from '../api/skills'
import { categoryLabels } from '../api/skills'

export interface SkillDetailProps {
  skill: Skill
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="grid grid-cols-[92px_minmax(0,1fr)] gap-2 border-b border-line px-3 py-2 last:border-b-0">
    <span className="text-[11px] leading-4 text-ink-3">{label}</span>
    <span className="min-w-0 text-[11.5px] leading-4 text-ink-2">{children}</span>
  </div>
}

/** Full context for one skill: what it does, what it may run, how to invoke it. */
export function SkillDetail({ skill }: SkillDetailProps) {
  return <section className="flex min-h-0 flex-1 flex-col overflow-auto bg-canvas">
    <header className="sticky top-0 z-10 border-b border-line bg-canvas/95 px-3 py-2 backdrop-blur">
      <h1 className="m-0 flex items-center gap-1.5 text-[14px] font-semibold leading-5">
        {skill.title}
        {skill.regentOnly && <span className="rounded-[4px] bg-accent-tint px-1 text-[9px] font-medium text-accent-ink">re_gent</span>}
        {!skill.installed && <span className="rounded-[4px] border border-line px-1 text-[9px] font-normal text-ink-3">proposed</span>}
      </h1>
      <p className="m-0 font-mono text-[10.5px] leading-4 text-accent-ink">*/skills/{skill.name}/SKILL.md</p>
    </header>

    <p className="m-0 px-3 py-2.5 text-[12px] leading-5 text-ink-2">{skill.description}</p>

    <div className="mx-3 mb-3 overflow-hidden rounded-[8px] border border-line bg-inset">
      <Row label="Category">{categoryLabels[skill.category]}</Row>
      <Row label="Reads">{skill.sources.join(', ')}</Row>
      <Row label="May run">
        <span className="flex flex-wrap gap-1">{skill.commands.map((command) => <code key={command} className="rounded-[4px] bg-field px-1 font-mono text-[10.5px] text-ink-2">{command}</code>)}</span>
      </Row>
      {skill.argumentHint && <Row label="Takes"><code className="font-mono text-[10.5px]">{skill.argumentHint}</code></Row>}
    </div>

    <div className="px-3 pb-3">
      <div className="mb-1 text-[11px] text-ink-3">Example</div>
      <pre className="m-0 overflow-x-auto rounded-[8px] border border-line bg-inset px-2.5 py-2 font-mono text-[11px] leading-5 text-ink-2">{skill.example}</pre>
    </div>

    {!skill.installed && <p className="mx-3 mb-3 mt-0 rounded-[8px] border border-line bg-inset px-2.5 py-2 text-[11px] leading-4 text-ink-3">
      Proposed, not yet written. Ask the <span className="text-ink-2">skill-factory</span> skill to build it, and it becomes a file an agent can load.
    </p>}
  </section>
}
