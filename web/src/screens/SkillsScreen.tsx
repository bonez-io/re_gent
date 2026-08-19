import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { categoryLabels, fetchSkills, installCommand, listSkills, serverBaseUrl, type Skill, type SkillCategory } from '../api/skills'
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
 * The skills marketplace.
 *
 * Reads the server's registry, falling back to the bundled catalog when no
 * registry answers. A skill published to the server appears here without
 * rebuilding this app — that is what makes the page a marketplace rather than
 * a list.
 *
 * Installing is one line: tick skills, copy `rgt skill install <names>`, paste
 * it in a terminal. The UI produces text and the CLI does the writing, so this
 * view stays read-only and the flow works in any harness.
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
  // Name the registry when the catalog came from one, so the copied command
  // installs the bytes this page showed rather than whatever the user's own
  // project happens to be bound to.
  const command = useMemo(() => installCommand(chosen, offline ? undefined : serverBaseUrl()), [chosen, offline])

  const toggle = (name: string, on: boolean) => {
    setCopied('idle')
    setChecked((current) => {
      const next = new Set(current)
      if (on) next.add(name)
      else next.delete(name)
      return next
    })
  }

  return <div className="grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)_340px] max-lg:grid-cols-1">
    <section className="relative flex min-h-0 flex-col overflow-hidden bg-inset">
      <div className="shrink-0 border-b border-line bg-canvas px-3 py-2">
        <div className="flex items-center gap-2">
          <h1 className="m-0 text-[13px] font-semibold leading-4">Skills</h1>
          <span className="text-[10.5px] tabular-nums text-ink-3">{all.length} available{localCount > 0 ? ` · ${localCount} published here` : ''}</span>
          {offline && <span title="The server has no skills registry; showing the catalog bundled with this build" className="rounded-[4px] border border-line px-1 text-[9px] text-ink-2">bundled</span>}
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Filter skills…" className="ml-auto h-7 w-44 rounded-[7px] bg-field px-2 text-[11.5px] outline-none shadow-hairline focus:shadow-btn" />
        </div>
        <div className="mt-1.5 flex flex-wrap items-center gap-1">
          {filters.map((item) => <button key={item} type="button" onClick={() => setFilter(item)} aria-pressed={filter === item} className={`h-6 rounded-[6px] px-1.5 text-[10.5px] transition-colors duration-150 ${filter === item ? 'bg-accent-tint text-accent-ink' : 'bg-field text-ink-3 hover:text-ink'}`}>
            {item === 'all' ? 'All' : categoryLabels[item]}
          </button>)}
        </div>
      </div>

      {/* Bottom padding clears the floating bar, so the last row is never hidden behind it. */}
      <div className="min-h-0 flex-1 overflow-auto p-3 pb-16">
        {visible.length
          ? <div className="grid grid-cols-[repeat(auto-fill,minmax(240px,1fr))] gap-2">
              {visible.map((skill) => <SkillCard
                key={skill.name}
                {...skill}
                selected={skill.name === selected?.name}
                onClick={() => setSelectedName(skill.name)}
                checked={checked.has(skill.name)}
                onCheckedChange={(on) => toggle(skill.name, on)}
              />)}
            </div>
          : <div className="py-10 text-center text-[11.5px] text-ink-3">No skills match that filter.</div>}
      </div>

      {chosen.length > 0 && <div className="pointer-events-none absolute inset-x-0 bottom-3 flex justify-center px-3">
        <div className="pointer-events-auto flex items-center gap-2 rounded-[10px] border border-line bg-canvas px-2.5 py-1.5 shadow-raised">
          <span className="text-[11.5px] font-medium tabular-nums">{chosen.length} selected</span>
          <button
            type="button"
            onClick={async () => setCopied(await copy(command) ? 'ok' : 'failed')}
            className="h-7 rounded-[7px] bg-accent-tint px-2.5 text-[11px] font-medium text-accent-ink shadow-hairline"
          >{copied === 'ok' ? 'Copied ✓' : copied === 'failed' ? 'Copy failed' : 'Copy command'}</button>
          <button type="button" onClick={() => { setChecked(new Set()); setCopied('idle') }} aria-label="Clear selection" className="flex size-7 items-center justify-center rounded-[7px] text-[13px] leading-none text-ink-2 hover:bg-hover hover:text-ink">×</button>
        </div>
      </div>}
    </section>

    <aside className="min-h-0 border-l border-line max-lg:border-l-0 max-lg:border-t">
      {selected ? <SkillDetail skill={selected} /> : <div className="px-3 py-10 text-center text-[11.5px] text-ink-3">Select a skill.</div>}
    </aside>
  </div>
}
