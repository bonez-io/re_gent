import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { categoryLabels, fetchSkills, installCommand, installPrompt, listSkills, type Skill, type SkillCategory } from '../api/skills'
import { SkillCard } from '../components/SkillCard'
import { SkillDetail } from '../components/SkillDetail'

type Filter = 'all' | SkillCategory

const filters: Filter[] = ['all', 'provenance', 'structure', 'conversation', 'meta']

/** Clipboard with a visible fallback: a copy button that silently fails is worse than none. */
async function copy(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

/**
 * The skills catalog.
 *
 * Reads the server's registry, falling back to the bundled catalog when no
 * registry answers. A skill published to the server appears here without
 * rebuilding this app — that is what makes the page a marketplace rather than
 * a list.
 *
 * Installation never happens here: the user ticks skills and copies
 * `rgt skill install <names>`. The UI produces text, the CLI does the writing,
 * so this view stays read-only and the flow works in any harness.
 */
export function SkillsScreen() {
  const registry = useQuery({ queryKey: ['skills'], queryFn: fetchSkills, staleTime: 30_000 })
  const all = registry.data?.skills ?? listSkills()
  const offline = registry.data?.offline ?? true
  const [filter, setFilter] = useState<Filter>('all')
  const [query, setQuery] = useState('')
  const [selectedName, setSelectedName] = useState<string>(all[0]?.name ?? '')
  const [checked, setChecked] = useState<ReadonlySet<string>>(() => new Set())
  const [copied, setCopied] = useState<'idle' | 'ok' | 'failed'>('idle')
  const [showPrompt, setShowPrompt] = useState(false)

  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return all.filter((skill) => {
      if (filter !== 'all' && skill.category !== filter) return false
      if (!needle) return true
      return skill.title.toLowerCase().includes(needle) || skill.description.toLowerCase().includes(needle) || skill.name.includes(needle)
    })
  }, [all, filter, query])

  const selected = all.find((skill) => skill.name === selectedName) ?? visible[0]
  const localCount = all.filter((skill) => skill.origin === 'local').length
  const chosen: Skill[] = useMemo(() => all.filter((skill) => checked.has(skill.name)), [all, checked])
  const prompt = useMemo(() => installPrompt(chosen), [chosen])
  const command = useMemo(() => installCommand(chosen), [chosen])

  const toggle = (name: string, on: boolean) => {
    setCopied('idle')
    setChecked((current) => {
      const next = new Set(current)
      if (on) next.add(name)
      else next.delete(name)
      return next
    })
  }

  const clear = () => { setChecked(new Set()); setCopied('idle'); setShowPrompt(false) }
  const checkAllVisible = () => { setCopied('idle'); setChecked(new Set(visible.map((skill) => skill.name))) }

  return <div className="grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)_340px] max-lg:grid-cols-1">
    <section className="flex min-h-0 flex-col overflow-hidden bg-inset">
      <div className="shrink-0 border-b border-line bg-canvas px-3 py-2">
        <div className="flex items-center gap-2">
          <h1 className="m-0 text-[13px] font-semibold leading-4">Skills</h1>
          <span className="text-[10.5px] tabular-nums text-ink-3">{all.length} available{localCount > 0 ? ` · ${localCount} published here` : ''}</span>
          {offline && <span title="The server has no skills registry; showing the catalog bundled with this build" className="rounded-[4px] border border-line px-1 text-[9px] text-ink-3">bundled</span>}
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Filter skills…" className="ml-auto h-7 w-44 rounded-[7px] bg-field px-2 text-[11.5px] outline-none shadow-hairline focus:shadow-btn" />
        </div>
        <div className="mt-1.5 flex flex-wrap items-center gap-1">
          {filters.map((item) => <button key={item} type="button" onClick={() => setFilter(item)} aria-pressed={filter === item} className={`h-6 rounded-[6px] px-1.5 text-[10.5px] transition-colors duration-150 ${filter === item ? 'bg-accent-tint text-accent-ink' : 'bg-field text-ink-3 hover:text-ink'}`}>
            {item === 'all' ? 'All' : categoryLabels[item]}
          </button>)}
          <button type="button" onClick={checkAllVisible} className="ml-auto h-6 rounded-[6px] bg-field px-1.5 text-[10.5px] text-ink-2 transition-colors hover:text-ink">Select all shown</button>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {visible.length
          ? <div className="grid grid-cols-[repeat(auto-fill,minmax(210px,1fr))] gap-2 p-3">
              {visible.map((skill) => <SkillCard
                key={skill.name}
                {...skill}
                selected={skill.name === selected?.name}
                onClick={() => setSelectedName(skill.name)}
                checked={checked.has(skill.name)}
                onCheckedChange={(on) => toggle(skill.name, on)}
              />)}
            </div>
          : <div className="px-3 py-10 text-center text-[11.5px] text-ink-3">No skills match that filter.</div>}

        <p className="m-0 border-t border-line px-3 py-2.5 text-[10.5px] leading-4 text-ink-3">
          Skills live at <code className="font-mono text-ink-2">.claude/skills/&lt;name&gt;/SKILL.md</code> and load when an agent starts.
          A skill is a prompt plus a tool grant — the analysis is the agent's, the history is re_gent's.
        </p>
      </div>

      {chosen.length > 0 && <div className="shrink-0 border-t border-line bg-canvas">
        <div className="flex flex-wrap items-center gap-2 px-3 py-2">
          <span className="text-[11.5px] font-medium tabular-nums">{chosen.length} selected</span>
          <span className="min-w-0 truncate text-[10.5px] text-ink-3">{chosen.map((skill) => skill.name).join(', ')}</span>
          <button type="button" onClick={() => setShowPrompt((open) => !open)} className="ml-auto h-7 rounded-[7px] bg-field px-2 text-[11px] text-ink-2 shadow-hairline hover:bg-hover-2">{showPrompt ? 'Hide prompt' : 'Show prompt'}</button>
          <button type="button" onClick={clear} className="h-7 rounded-[7px] px-2 text-[11px] text-ink-2 hover:text-ink">Clear</button>
          <button
            type="button"
            onClick={async () => setCopied(await copy(prompt) ? 'ok' : 'failed')}
            className="h-7 rounded-[7px] bg-field px-2.5 text-[11px] text-ink-2 shadow-hairline hover:bg-hover-2"
          >Copy prompt</button>
          <button
            type="button"
            onClick={async () => setCopied(await copy(command) ? 'ok' : 'failed')}
            className="h-7 rounded-[7px] bg-accent-tint px-2.5 text-[11px] font-medium text-accent-ink shadow-hairline"
          >{copied === 'ok' ? 'Copied ✓' : copied === 'failed' ? 'Copy failed — select below' : 'Copy command'}</button>
        </div>
        <div className="border-t border-line bg-inset px-3 py-2">
          <code className="block select-all break-all font-mono text-[11px] leading-4 text-accent-ink">{command}</code>
        </div>
        {/* tabIndex: the block scrolls, so a keyboard user must be able to focus and scroll it too. */}
        {(showPrompt || copied === 'failed') && <pre tabIndex={0} aria-label="Install prompt" className="m-0 max-h-40 select-all overflow-auto border-t border-line bg-inset px-3 py-2 font-mono text-[10.5px] leading-4 text-ink-2 focus:outline-none focus:shadow-btn">{prompt}</pre>}
      </div>}
    </section>

    <aside className="min-h-0 border-l border-line max-lg:border-l-0 max-lg:border-t">
      {selected ? <SkillDetail skill={selected} /> : <div className="px-3 py-10 text-center text-[11.5px] text-ink-3">Select a skill.</div>}
    </aside>
  </div>
}
