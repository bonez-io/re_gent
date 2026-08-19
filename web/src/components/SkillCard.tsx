import type { Skill } from '../api/skills'

export type SkillCardProps = Skill & {
  selected?: boolean
  onClick?: () => void
  /** Ticked for install. Distinct from `selected`, which drives the detail panel. */
  checked?: boolean
  onCheckedChange?: (checked: boolean) => void
}

/** The one badge a card carries: whether this skill is on offer, published here, or held back. */
function StatusBadge({ installed, origin, withheld }: Pick<Skill, 'installed' | 'origin' | 'withheld'>) {
  if (withheld) return <span title={withheld} className="shrink-0 rounded-[4px] border border-line px-1 text-[9px] text-ink-2">withheld</span>
  if (origin === 'local') return <span title="Published to this server" className="shrink-0 rounded-[4px] bg-accent-tint px-1 text-[9px] text-accent-ink">published</span>
  if (!installed) return <span className="shrink-0 rounded-[4px] border border-line px-1 text-[9px] text-ink-2">proposed</span>
  return <span className="shrink-0 rounded-[4px] bg-field px-1 text-[9px] text-ink-2">available</span>
}

/**
 * One skill in the marketplace grid.
 *
 * Deliberately thin: a title, a line of description, and one badge. Category
 * and data sources drive the filters above the grid and are shown in the detail
 * panel — repeating them on every card turned a browsable grid into a wall of
 * metadata, which is the opposite of what a marketplace is for.
 *
 * Two independent affordances share the card: the body opens the detail panel,
 * the checkbox marks the skill for installation. They stay separate controls
 * because "show me this" and "give me this" are different intentions.
 */
export function SkillCard({ title, description, installed, origin, withheld, selected = false, onClick, checked = false, onCheckedChange }: SkillCardProps) {
  return <div className={`group flex items-start gap-2 rounded-[9px] border p-2.5 transition-[background-color,box-shadow] duration-150 ${checked ? 'border-accent/40 bg-accent-tint/25' : selected ? 'border-line bg-hover-2 shadow-hairline' : 'border-line bg-canvas hover:bg-hover'}`}>
    <label className={`flex items-center pt-0.5 ${installed ? 'cursor-pointer' : 'cursor-not-allowed opacity-45'}`}>
      <input
        type="checkbox"
        checked={checked}
        disabled={!installed}
        onChange={(event) => onCheckedChange?.(event.target.checked)}
        aria-label={`Select ${title} for install`}
        className="size-3.5 cursor-pointer accent-[var(--color-accent,#7c6cff)] disabled:cursor-not-allowed"
      />
    </label>
    <button type="button" onClick={onClick} aria-current={selected ? 'page' : undefined} className="min-w-0 flex-1 text-left">
      <span className="flex items-center gap-1.5">
        <span className="truncate text-[12.5px] font-semibold leading-4 text-ink">{title}</span>
        <StatusBadge installed={installed} origin={origin} withheld={withheld} />
      </span>
      <span className="mt-0.5 line-clamp-1 text-[11px] leading-4 text-ink-3">{description}</span>
    </button>
  </div>
}
