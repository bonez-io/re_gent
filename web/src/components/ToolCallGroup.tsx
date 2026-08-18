import { useState } from 'react'

export type ToolCall = { id: string; tool: string; summary: string; detail?: string[]; status?: 'complete' | 'failed' }
export type FileChange = { path: string; additions: number; deletions: number }
export interface ToolCallGroupProps { calls: ToolCall[]; files?: FileChange[]; defaultOpen?: boolean }

function ToolIcon({ failed }: { failed?: boolean }) {
  return <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={failed ? 'text-red' : 'text-ink-3'}><path d="M4 17l6-5-6-5M12 19h8" /></svg>
}

/** Adapted from Beautiful UI's MIT-licensed Tool Chips primitive. */
export function ToolCallGroup({ calls, files = [], defaultOpen = true }: ToolCallGroupProps) {
  const [open, setOpen] = useState(defaultOpen)
  const [openRows, setOpenRows] = useState<Set<string>>(new Set())
  const toggleRow = (id: string) => setOpenRows((current) => {
    const next = new Set(current)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    return next
  })
  return <div className="w-full max-w-xl pb-1">
    <button type="button" aria-expanded={open} onClick={() => setOpen((value) => !value)} className="-mx-1.5 flex w-fit items-center gap-1.5 rounded-control px-1.5 py-1 text-[12.5px] text-ink-2 transition-colors hover:bg-hover-2">
      <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" className="transition-transform duration-200" style={{ transform: open ? 'rotate(0deg)' : 'rotate(-90deg)' }}><path d="M6 9l6 6 6-6" /></svg>
      <span>{calls.length} tool calls · {files.length} changed files</span>
    </button>
    <div className="grid transition-[grid-template-rows,opacity] duration-300" style={{ gridTemplateRows: open ? '1fr' : '0fr', opacity: open ? 1 : 0 }}><div className="min-h-0 overflow-hidden">
      <div className="mt-1.5 flex flex-col gap-1">
        {calls.map((call) => { const rowOpen = openRows.has(call.id); return <div key={call.id}>
          <button type="button" aria-expanded={rowOpen} onClick={() => toggleRow(call.id)} className="group flex h-7 w-full min-w-0 items-center gap-2 rounded-control px-1 text-left transition-colors hover:bg-hover-2">
            <span className="flex size-4 shrink-0 items-center justify-center"><ToolIcon failed={call.status === 'failed'} /></span>
            <span className="shrink-0 text-[12.5px] font-medium text-ink">{call.tool}</span>
            <span className="inline-flex h-5.5 min-w-0 flex-1 items-center truncate rounded-chip bg-field px-1.5 font-mono text-[11.5px] text-ink-2 shadow-hairline">{call.summary}</span>
            <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" className="text-ink-3 opacity-0 transition-[opacity,transform] group-hover:opacity-100" style={{ transform: rowOpen ? 'rotate(0deg)' : 'rotate(-90deg)' }}><path d="M6 9l6 6 6-6" /></svg>
          </button>
          <div className="grid transition-[grid-template-rows,opacity] duration-300" style={{ gridTemplateRows: rowOpen ? '1fr' : '0fr', opacity: rowOpen ? 1 : 0 }}><div className="min-h-0 overflow-hidden"><div className="my-1 ml-2 flex flex-col border-l border-line py-0.5 pl-3.5 font-mono text-[11.5px] leading-[1.7] text-ink-2">{call.detail?.map((line) => <span key={line} className={call.status === 'failed' ? 'text-red' : ''}>{line}</span>)}</div></div></div>
        </div> })}
      </div>
      {files.length > 0 && <div className="mt-2.5 flex flex-wrap gap-1.5 border-t border-line pt-2.5">{files.map((file) => <button key={file.path} type="button" className="inline-flex h-7 max-w-full items-center gap-1.5 rounded-chip bg-surface px-2 font-mono text-[11.5px] text-ink shadow-btn transition-colors hover:bg-hover"><span className="truncate">{file.path}</span><span className="text-green">+{file.additions}</span>{file.deletions > 0 && <span className="text-red">−{file.deletions}</span>}</button>)}</div>}
    </div></div>
  </div>
}
