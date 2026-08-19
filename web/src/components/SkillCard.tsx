import type { Skill } from '../api/skills'
import { categoryLabels } from '../api/skills'

export type SkillCardProps = Skill & {
  selected?: boolean
  onClick?: () => void
  /** Ticked for install. Distinct from `selected`, which drives the detail panel. */
  checked?: boolean
  onCheckedChange?: (checked: boolean) => void
}

/**
 * One skill in the catalog grid.
 *
 * Two independent affordances share the card: the body opens the detail panel,
 * the checkbox marks the skill for installation. They are separate controls
 * rather than one overloaded click, because "show me this" and "give me this"
 * are different intentions and a card that conflates them makes both harder.
 *
 * Carries the fields a marketplace needs — category, sources, install state —
 * even while the catalog is bundled, because retrofitting them into a card
 * after the layout is fixed is the expensive path.
 */
export function SkillCard({ name, title, description, category, sources, installed, regentOnly, origin, withheld, selected = false, onClick, checked = false, onCheckedChange }: SkillCardProps) {
  return <div className={`group relative flex h-full min-h-27 flex-col rounded-[9px] border p-2.5 transition-[background-color,box-shadow] duration-150 ${checked ? 'border-accent/40 bg-accent-tint/25' : selected ? 'border-line bg-hover-2 shadow-hairline' : 'border-line bg-canvas hover:bg-hover'}`}>
    <div className="flex w-full items-start gap-1.5">
      <label className="flex cursor-pointer items-center pt-px" onClick={(event) => event.stopPropagation()}>
        <input
          type="checkbox"
          checked={checked}
          onChange={(event) => onCheckedChange?.(event.target.checked)}
          aria-label={`Select ${title} for install`}
          className="size-3.5 cursor-pointer accent-[var(--color-accent,#7c6cff)]"
        />
      </label>
      <button type="button" onClick={onClick} aria-current={selected ? 'page' : undefined} className="flex min-w-0 flex-1 items-center gap-1.5 text-left">
        <span className="truncate text-[12.5px] font-semibold leading-4 text-ink">{title}</span>
        {regentOnly && <span title="Answers a question Git cannot" className="shrink-0 rounded-[4px] bg-accent-tint px-1 text-[9px] font-medium text-accent-ink">re_gent</span>}
      </button>
      {withheld
        ? <span title={withheld} className="shrink-0 rounded-[4px] border border-line px-1 text-[9px] text-ink-3">withheld</span>
        : origin === 'local'
          ? <span title="Published to this server" className="shrink-0 rounded-[4px] bg-accent-tint px-1 text-[9px] text-accent-ink">published</span>
          : <span className={`shrink-0 rounded-[4px] px-1 text-[9px] ${installed ? 'bg-field text-ink-3' : 'border border-line text-ink-3'}`}>{installed ? 'available' : 'proposed'}</span>}
    </div>

    <button type="button" onClick={onClick} className="mt-1 flex flex-1 flex-col text-left">
      <span className="line-clamp-3 text-[11px] leading-4 text-ink-3">{description}</span>
      <span className="mt-auto flex w-full items-center gap-1 pt-2">
        <span className="rounded-[4px] bg-field px-1 text-[9px] text-ink-2">{categoryLabels[category]}</span>
        <span className="truncate text-[9.5px] text-ink-3">{sources.join(' · ')}</span>
        <span className="ml-auto shrink-0 font-mono text-[9.5px] text-ink-3">{name}</span>
      </span>
    </button>
  </div>
}
