export interface StepMarkerProps { hash: string; tree: string; turn: string; tokens: number; files: number; at?: string }

/** Immutable re_gent checkpoint separating captured turns. */
export function StepMarker({ hash, tree, turn, tokens, files, at }: StepMarkerProps) {
  return <div className="my-1 flex min-h-8 items-center gap-2 border-y border-line bg-inset px-2.5 text-[9.5px] text-ink-3" aria-label={`Step ${hash}`}>
    <span className="size-1.5 rounded-full bg-accent" />
    <span className="font-semibold uppercase tracking-[0.08em] text-ink-2">Step</span>
    <span className="font-mono text-accent-ink">{hash}</span>
    <span className="hidden sm:inline">tree <span className="font-mono">{tree}</span></span>
    <span className="hidden md:inline">{turn}</span>
    <span className="ml-auto tabular-nums">{tokens.toLocaleString()} tok · {files} files{at ? ` · ${at}` : ''}</span>
  </div>
}
