export interface StepMarkerProps { hash: string; tree: string; turn: string; tokens: number; files: number; at?: string }

/** Immutable re_gent checkpoint separating captured turns. */
export function StepMarker({ hash, tree, turn, tokens, files, at }: StepMarkerProps) {
  return <div className="mx-6 my-8 border-b border-line pb-3 text-[10.5px] text-ink-3" aria-label={`Step ${hash}`}>
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <span className="h-2.5 w-1 bg-ink-3/70" />
      <span className="font-medium uppercase tracking-[0.08em] text-ink-3">Step</span>
      <span className="font-mono text-ink-2">{hash}</span>
      <span className="hidden sm:inline">tree <span className="font-mono">{tree}</span></span>
      <span className="hidden md:inline">{turn}</span>
      <span className="font-mono tabular-nums">{tokens.toLocaleString()} tok · {files} files{at ? ` · ${at}` : ''}</span>
    </div>
  </div>
}
