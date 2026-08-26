import type { Ref } from 'react'
import { Link } from 'react-router-dom'

export interface StepMarkerProps { hash: string; tree: string; turn: string; tokens: number; files: number; at?: string; targeted?: boolean; highlighted?: boolean; markerRef?: Ref<HTMLDivElement>; repoId?: string; fullHash?: string }

// The step id and the tree value both open the same destination: the file browser as of this
// step. `hash` is only an 8-char display prefix (see api/adapters.ts), so the href needs the
// full hash separately.
const filesHref = (repoId: string, fullHash: string) => `/repos/${encodeURIComponent(repoId)}/files?step=${encodeURIComponent(fullHash)}`

/**
 * Immutable re_gent checkpoint separating captured turns. Concise by default — a hairline rule
 * with the step id centered on it; tree/turn/tokens/files/time reveal on hover or focus-within.
 * The detail row stays rendered (opacity only, never `hidden`/unmounted) so it never reflows the
 * transcript below it and stays in the a11y tree for screen-reader users at all times.
 *
 * `targeted` is the "arrived here via a blame link" state: unlike hover/focus-within it does not
 * depend on the pointer staying put, so a step deep-linked from Browse stays open once scrolled to.
 * `highlighted` is the transient flash that draws the eye on arrival and then releases, so the
 * accent does not sit on the step forever after the user has already found it.
 */
export function StepMarker({ hash, tree, turn, tokens, files, at, targeted, highlighted, markerRef, repoId, fullHash }: StepMarkerProps) {
  const href = repoId && fullHash ? filesHref(repoId, fullHash) : undefined
  const idLabel = `Browse files at step ${hash}${targeted ? ', linked from blame' : ''}`
  const chipClassName = `flex items-center gap-1.5 rounded-[4px] px-3 font-mono text-[11.5px] transition-colors duration-700 motion-reduce:transition-none ${highlighted ? 'bg-accent-tint text-accent-ink' : 'bg-canvas text-ink-2'}`
  const tick = <span className={`h-2.5 w-1 transition-colors duration-700 motion-reduce:transition-none ${highlighted ? 'bg-accent' : 'bg-ink-3/70'}`} aria-hidden />

  // tabIndex only when targeted: a step marker is not part of the normal tab order, but a blame
  // link needs a programmatic focus() target so keyboard/screen-reader users land here too.
  return <div ref={markerRef} tabIndex={targeted ? -1 : undefined} aria-current={targeted ? 'location' : undefined} className="group relative mx-6 my-8 outline-none" aria-label={`Step ${hash}${targeted ? ', linked from blame' : ''}`}>
    <div className="absolute inset-x-0 top-1/2 h-px -translate-y-1/2 bg-line" aria-hidden />
    <div className="relative flex justify-center">
      {/* Long transition so the flash releases gently rather than snapping off. It only runs on
          the way out: the marker mounts already highlighted, and mount has nothing to animate.
          When repoId/fullHash aren't supplied there is no valid destination, so this renders as
          plain text rather than a link to nowhere. */}
      {href
        ? <Link to={href} aria-label={idLabel} className={`${chipClassName} hover:bg-hover hover:text-ink focus-visible:bg-hover focus-visible:text-ink`}>{tick}{hash}</Link>
        : <span className={chipClassName} aria-label={`Step ${hash} details${targeted ? ', linked from blame' : ''}`}>{tick}{hash}</span>}
    </div>
    <div className={`mt-1.5 flex flex-wrap items-center justify-center gap-x-2 gap-y-1 text-center font-mono text-[10.5px] text-ink-3 transition-opacity duration-150 motion-reduce:transition-none group-hover:opacity-100 group-focus-within:opacity-100 ${targeted ? 'opacity-100' : 'opacity-0'}`}>
      {/* Same destination as the step id above — tabIndex={-1} keeps it out of sequential tab
          order so a keyboard user doesn't hit two stops for one link per step in a long session,
          while it stays reachable by click, by screen-reader browse navigation, and after the
          id link has already brought focus-within here. */}
      <span className="hidden sm:inline">tree {href
        ? <Link to={href} tabIndex={-1} aria-label={`View the tree at step ${hash}`} className="text-ink-2 transition-colors hover:text-ink hover:underline focus-visible:text-ink focus-visible:underline">{tree}</Link>
        : <span className="text-ink-2">{tree}</span>}</span>
      <span className="hidden md:inline">{turn}</span>
      <span className="tabular-nums">{tokens.toLocaleString()} tok · {files} files{at ? ` · ${at}` : ''}</span>
    </div>
  </div>
}
