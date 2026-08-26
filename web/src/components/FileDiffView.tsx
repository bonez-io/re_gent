import { useMemo, useState } from 'react'
import { languageForPath, useCodeTokens, type ThemedToken } from '../lib/highlight'
import { CodeLine } from './CodeView'

export type DiffLineKind = 'context' | 'add' | 'delete'
export interface DiffLine { kind: DiffLineKind; old_number?: number; new_number?: number; content: string }
export interface DiffHunk { old_start: number; old_lines: number; new_start: number; new_lines: number; lines: DiffLine[] }
export interface FileDiff {
  path: string
  status: 'added' | 'modified' | 'deleted'
  is_binary: boolean
  truncated: boolean
  additions: number
  deletions: number
  hunks: DiffHunk[]
}
export interface FileDiffViewProps {
  diff: FileDiff
  /** Rendered as the file link target; when omitted the path is plain text, not a dead link. */
  href?: string
  /** Starts collapsed when false. */
  defaultOpen?: boolean
}

const STATUS_LABEL: Record<FileDiff['status'], string> = { added: 'Added', modified: 'Modified', deleted: 'Deleted' }
const STATUS_DOT: Record<FileDiff['status'], string> = { added: 'bg-green', modified: 'bg-ink-3', deleted: 'bg-red' }

function Chevron({ open }: { open: boolean }) {
  return <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.3" className="shrink-0 text-ink-3 transition-transform duration-200" style={{ transform: open ? 'rotate(0deg)' : 'rotate(-90deg)' }}><path d="M6 9l6 6 6-6" /></svg>
}

/** The `@@ -old,+new @@` idiom from unified diffs — quiet, but tells the reader exactly
 *  how many lines were skipped between two hunks without us having to fabricate a count. */
function HunkSeparator({ hunk }: { hunk: DiffHunk }) {
  return <div className="border-t border-line bg-inset px-2.5 py-1 font-mono text-[10px] text-ink-3">
    @@ -{hunk.old_start},{hunk.old_lines} +{hunk.new_start},{hunk.new_lines} @@
  </div>
}

const MARKER: Record<DiffLineKind, string> = { add: '+', delete: '−', context: '' }
const TINT: Record<DiffLineKind, string> = { add: 'bg-green-tint', delete: 'bg-red-tint', context: '' }

/** One diff row. `tokens` is only ever passed for context lines — see the note on
 *  `newSource` below for why add/delete lines always render as plain text. */
function DiffRow({ line, tokens }: { line: DiffLine; tokens?: ThemedToken[] }) {
  return <div className={`grid min-w-max min-h-4 grid-cols-[32px_32px_14px_minmax(0,1fr)] items-baseline ${TINT[line.kind]}`}>
    <span className="select-none pr-1.5 text-right font-mono text-[10px] text-ink-2">{line.old_number ?? ''}</span>
    <span className="select-none pr-1.5 text-right font-mono text-[10px] text-ink-2">{line.new_number ?? ''}</span>
    <span className="select-none text-center font-mono text-[10.5px] font-semibold text-ink">{MARKER[line.kind]}</span>
    <CodeLine tokens={tokens} text={line.content} className="pr-3" />
  </div>
}

/**
 * Compact collapsible per-file diff, rendered from a structured `FileDiff` (already computed
 * server-side) rather than a raw patch string — this component only lays it out.
 */
export function FileDiffView({ diff, href, defaultOpen = true }: FileDiffViewProps) {
  const [open, setOpen] = useState(defaultOpen)

  // Deleted files have no content left to link to at this step: `href` would point at a 404,
  // or worse, at a later step that recreated the same path with unrelated content. So a
  // deleted file's path is never a link, regardless of whether the caller passed one.
  const linkable = Boolean(href) && diff.status !== 'deleted'

  const language = useMemo(() => languageForPath(diff.path), [diff.path])
  // Highlighting only ever runs on context lines, and only while the block is open. Added and
  // deleted lines always render as plain text (still upgraded-in-place via CodeLine, just never
  // tokenized) because the shiki themes here were measured for contrast against the app's plain
  // canvas/inset backgrounds, not against the green-tint/red-tint row backgrounds — a token color
  // that clears AA on inset is not guaranteed to clear it on a tinted row, and we've already had
  // to drop one theme for exactly this kind of unverified contrast. Restricting tokens to context
  // rows (which do sit on the vetted background) keeps every rendered color inside what was
  // actually measured, at the cost of add/delete lines losing syntax color.
  const newSource = useMemo(() => {
    if (diff.is_binary || !open) return ''
    const parts: string[] = []
    for (const hunk of diff.hunks) for (const line of hunk.lines) if (line.kind !== 'delete') parts.push(line.content)
    return parts.join('\n')
  }, [diff.hunks, diff.is_binary, open])
  const { lines: newTokens } = useCodeTokens(newSource, language)

  // `newPos` walks every context/add line in file order (the same order `newSource` was built
  // in), so it lines back up with `newTokens` regardless of how lines are split across hunks.
  let newPos = 0
  const hunkBlocks = diff.hunks.map((hunk, hunkIndex) => {
    const rows = hunk.lines.map((line, lineIndex) => {
      const tokens = line.kind === 'context' ? newTokens?.[newPos] : undefined
      if (line.kind !== 'delete') newPos++
      return <DiffRow key={`${hunkIndex}-${lineIndex}`} line={line} tokens={tokens} />
    })
    return <div key={`hunk-${hunkIndex}`}>{hunkIndex > 0 && <HunkSeparator hunk={hunk} />}{rows}</div>
  })

  return <div className="w-full max-w-[920px] overflow-hidden rounded-[4px] border border-line bg-canvas shadow-hairline">
    <div className="flex items-center border-b border-line">
      {/* The toggle covers everything except the path itself when the path is a link — an <a>
          can't nest inside a <button>, so a linkable path is a sibling, not a child. */}
      <button type="button" aria-expanded={open} aria-label={`${open ? 'Collapse' : 'Expand'} ${diff.path} diff`}
        onClick={() => setOpen((value) => !value)}
        className="flex min-w-0 flex-1 items-center gap-2 px-2.5 py-1.5 text-left hover:bg-hover">
        <Chevron open={open} />
        <span className={`size-1.5 shrink-0 rounded-full ${STATUS_DOT[diff.status]}`} />
        <span className="shrink-0 text-[10.5px] text-ink-3">{STATUS_LABEL[diff.status]}</span>
        {!linkable && <span className="min-w-0 flex-1 truncate font-mono text-[11.5px] text-ink">{diff.path}</span>}
      </button>
      {linkable && <a href={href} title={diff.path} className="min-w-0 flex-1 truncate px-1 font-mono text-[11.5px] text-ink hover:underline">{diff.path}</a>}
      <span className="shrink-0 px-2.5 font-mono text-[10.5px]">
        <span className="text-green">+{diff.additions}</span>
        {diff.deletions > 0 && <span className="ml-1 text-red">−{diff.deletions}</span>}
      </span>
    </div>

    {/* tabIndex makes the internal scroll region reachable from the keyboard — axe's
        scrollable-region-focusable rule requires any independently-scrollable box to be
        focusable, since it can't otherwise be scrolled without a mouse/trackpad. */}
    {open && <div tabIndex={0} className="max-h-[420px] overflow-auto font-mono text-[11px] leading-[1.35]">
      {diff.truncated && <p className="m-0 border-b border-line bg-field px-2.5 py-1.5 text-[10.5px] text-ink-3">Diff truncated — showing partial changes only.</p>}
      {diff.is_binary
        ? <p className="m-0 px-2.5 py-3 text-[11.5px] text-ink-3">Binary file — preview not available.</p>
        : diff.hunks.length === 0
          ? <p className="m-0 px-2.5 py-3 text-[11.5px] text-ink-3">No textual changes.</p>
          : <div className="py-0.5">{hunkBlocks}</div>}
    </div>}
  </div>
}
