export type BlameLine = { number: number; hash: string; author: string; code: string }
export interface FilesBlameProps { lines: BlameLine[]; selectedHash?: string }

const files = [
  ['src/reminders', ''], ['parser.ts', '+7 −4'], ['parser.test.ts', '+46'], ['service.ts', '+11 −3'],
  ['src/context', ''], ['profile.ts', ''], ['package.json', ''],
]

export function FilesBlame({ lines, selectedHash = '7ac3ef1' }: FilesBlameProps) {
  return <div className="grid min-h-0 flex-1 grid-cols-[220px_minmax(0,1fr)] max-md:grid-cols-1">
    <aside className="border-r border-line bg-canvas max-md:hidden" aria-label="Changed files">
      <div className="flex h-9 items-center border-b border-line px-2.5 text-[10.5px] font-semibold">Files <span className="ml-auto font-normal text-ink-3">7 changed</span></div>
      <div className="py-1">{files.map(([file, stat], index) => <button key={`${file}-${index}`} className={`flex h-6.5 w-full items-center gap-1.5 px-2.5 text-left font-mono text-[10px] ${file === 'parser.ts' ? 'bg-hover-2 text-ink' : 'text-ink-3 hover:bg-hover'}`}><span className={stat ? 'ml-3' : ''}>{stat ? '·' : '⌄'}</span><span className="min-w-0 flex-1 truncate">{file}</span><span className="text-[9px] text-green">{stat}</span></button>)}</div>
    </aside>
    <section className="min-w-0 bg-inset">
      <div className="flex h-9 items-center gap-2 border-b border-line bg-canvas px-3"><span className="font-mono text-[10.5px] text-ink-2">src/reminders/parser.ts</span><span className="text-[9.5px] text-ink-3">at step</span><span className="font-mono text-[10px] text-accent-ink">{selectedHash}</span><span className="ml-auto text-[9.5px] text-ink-3">Blame view</span></div>
      <div className="overflow-auto py-2 font-mono text-[10.5px] leading-6">{lines.map((line) => <div key={line.number} className={`grid min-w-[640px] grid-cols-[66px_48px_38px_minmax(0,1fr)] border-l-2 px-2 ${line.hash === selectedHash ? 'border-accent bg-accent-tint/45' : 'border-transparent'}`}><button className={`text-left ${line.hash === selectedHash ? 'text-accent-ink' : 'text-ink-3'}`}>{line.hash}</button><span className="truncate text-[9.5px] text-ink-3">{line.author}</span><span className="select-none text-right text-ink-3">{line.number}</span><code className="pl-3 text-ink-2">{line.code}</code></div>)}</div>
      <div className="mx-3 mt-4 border border-line bg-canvas">
        <div className="flex h-8 items-center border-b border-line px-2.5 text-[10px]"><span className="font-semibold">Provenance</span><span className="ml-auto font-mono text-accent-ink">7ac3ef1</span></div>
        <dl className="grid grid-cols-[92px_minmax(0,1fr)] text-[10px] [&>dd]:m-0 [&>dd]:border-b [&>dd]:border-line [&>dd]:px-2.5 [&>dd]:py-1.5 [&>dt]:border-b [&>dt]:border-line [&>dt]:bg-inset [&>dt]:px-2.5 [&>dt]:py-1.5 [&>dt]:text-ink-3"><dt>Prompt</dt><dd>Fix premature timezone conversion and keep the change narrow.</dd><dt>Session</dt><dd className="font-mono">codex:01JZQ8MX7D</dd><dt>Cause</dt><dd className="font-mono">Edit · src/reminders/parser.ts</dd><dt>Parent</dt><dd className="font-mono">41ac200</dd></dl>
      </div>
    </section>
  </div>
}
