import claudeCodeMono from '@lobehub/icons-static-svg/icons/claudecode.svg?raw'
import claudeCodeColor from '@lobehub/icons-static-svg/icons/claudecode-color.svg?raw'
import codexMono from '@lobehub/icons-static-svg/icons/codex.svg?raw'
import codexColor from '@lobehub/icons-static-svg/icons/codex-color.svg?raw'
import opencodeMono from '@lobehub/icons-static-svg/icons/opencode.svg?raw'
import piMono from '@lobehub/icons-static-svg/icons/pi.svg?raw'

interface AgentMark { mono: string; color?: string; label: string }

const AGENTS: Record<string, AgentMark> = {
  claude_code: { mono: claudeCodeMono, color: claudeCodeColor, label: 'Claude Code' },
  codex: { mono: codexMono, color: codexColor, label: 'Codex' },
  opencode: { mono: opencodeMono, label: 'OpenCode' },
  pi: { mono: piMono, label: 'Pi' },
}

/** Neutral diamond mark for an origin we don't recognize yet. */
const FALLBACK_SVG = '<svg fill="currentColor" fill-rule="evenodd" height="1em" style="flex:none;line-height:1" viewBox="0 0 24 24" width="1em" xmlns="http://www.w3.org/2000/svg"><path d="M12 2 22 12 12 22 2 12z"></path></svg>'

const stripTitle = (svg: string) => svg.replace(/<title>[^<]*<\/title>/, '')

/** Brand hue per vendor, for tinting the currentColor marks. Lives here beside the icons
 *  so the blame gutter and the session header cannot drift to different palettes. */
const TINT: Record<string, string> = {
  claude_code: '#d97757',
  codex: '#10a37f',
  opencode: '#5b8def',
  pi: '#a78bfa',
}

/** Tint for an origin's mark; unrecognized origins stay muted rather than borrowing a brand. */
export const agentColor = (origin?: string) => TINT[origin ?? ''] ?? 'var(--ink-3)'

/** Human-readable vendor name for an origin, falling back to a title-cased guess.
 *  "sync" is not an agent at all — it marks lines captured from a workspace snapshot
 *  taken outside any agent turn, so it reads as "baseline" everywhere origin is shown. */
export function agentLabel(origin?: string): string {
  if (origin === 'sync') return 'baseline'
  const known = origin ? AGENTS[origin] : undefined
  if (known) return known.label
  const guess = (origin ?? '').replace(/[_-]+/g, ' ').trim()
  if (!guess) return 'Unknown agent'
  return guess[0].toUpperCase() + guess.slice(1)
}

export interface AgentIconProps {
  origin?: string
  className?: string
  /** Prefer the brand-colored variant, where one exists. */
  color?: boolean
  /** True when adjacent visible text already names the vendor. */
  decorative?: boolean
}

/** Inline vendor mark for a captured session's origin (claude_code, codex, opencode, pi, ...). */
export function AgentIcon({ origin, className = '', color = false, decorative = false }: AgentIconProps) {
  const mark = origin ? AGENTS[origin] : undefined
  const svg = stripTitle(mark ? (color && mark.color ? mark.color : mark.mono) : FALLBACK_SVG)
  const a11y = decorative ? { 'aria-hidden': true as const } : { role: 'img' as const, 'aria-label': agentLabel(origin) }
  return <span className={`inline-flex ${className}`.trim()} dangerouslySetInnerHTML={{ __html: svg }} {...a11y} />
}
