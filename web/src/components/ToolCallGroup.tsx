import { useState } from 'react'

export type ToolCall = { id: string; tool: string; summary: string; detail?: string[]; status?: 'complete' | 'failed' }
export type FileChange = { path: string; additions: number; deletions: number }
export interface ToolCallGroupProps { calls: ToolCall[]; files?: FileChange[]; defaultOpen?: boolean }

function ToolIcon({ tool, failed }: { tool: string; failed?: boolean }) {
  const path = tool === 'Read' ? <><path d="M6 3h9l3 3v15H6z" /><path d="M9 10h6M9 14h6" /></> : tool === 'Search' ? <><circle cx="10.5" cy="10.5" r="6" /><path d="m15 15 5 5" /></> : tool === 'Edit' ? <><path d="m4 20 4.5-1 10-10-3.5-3.5-10 10zM13.5 7l3.5 3.5" /></> : <><path d="M4 17l6-5-6-5M12 19h8" /></>
  return <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" className={failed ? 'text-red' : 'text-ink-3'}>{path}</svg>
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
  return <div className="w-full pb-0.5">
    <button type="button" aria-expanded={open} onClick={() => setOpen((value) => !value)} className="-mx-1 flex w-fit items-center gap-1 rounded-[4px] px-1 py-0.5 text-[11.5px] text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2">
      <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" className="transition-transform duration-200" style={{ transform: open ? 'rotate(0deg)' : 'rotate(-90deg)' }}><path d="M6 9l6 6 6-6" /></svg>
      <span>{calls.length} tool calls{files.length > 0 ? ` · ${files.length} changed files` : ''}</span>
    </button>
    <div className="grid transition-[grid-template-rows,opacity] duration-300" style={{ gridTemplateRows: open ? '1fr' : '0fr', opacity: open ? 1 : 0 }}><div className="min-h-0 overflow-hidden">
      <div className="mt-1 flex flex-col gap-px border-l border-line pl-2.5">
        {calls.map((call) => { const rowOpen = openRows.has(call.id); return <div key={call.id}>
          <button type="button" aria-expanded={rowOpen} onClick={() => toggleRow(call.id)} className="group flex h-7 w-full min-w-0 items-center gap-1.5 rounded-[4px] px-1 text-left transition-colors hover:bg-hover-2">
            <span className="flex size-3.5 shrink-0 items-center justify-center"><ToolIcon tool={call.tool} failed={call.status === 'failed'} /></span>
            <span className="w-11 shrink-0 text-[11.5px] font-medium text-ink-2">{call.tool}</span>
            <span className="min-w-0 flex-1 truncate font-mono text-[11.5px] text-ink-3">{call.summary}</span>
            <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" className="text-ink-3 opacity-0 transition-[opacity,transform] group-hover:opacity-100" style={{ transform: rowOpen ? 'rotate(0deg)' : 'rotate(-90deg)' }}><path d="M6 9l6 6 6-6" /></svg>
          </button>
          <div className="grid transition-[grid-template-rows,opacity] duration-200" style={{ gridTemplateRows: rowOpen ? '1fr' : '0fr', opacity: rowOpen ? 1 : 0 }}><div className="min-h-0 overflow-hidden"><div className="my-0.5 ml-5 flex flex-col border-l border-line py-0.5 pl-2.5 font-mono text-[11px] leading-[1.5] text-ink-2">{call.detail?.map((line) => <span key={line} className={call.status === 'failed' ? 'text-red' : ''}>{line}</span>)}</div></div></div>
        </div> })}
      </div>
      {files.length > 0 && <div className="mt-1.5 flex flex-wrap gap-1">{files.map((file) => <button key={file.path} type="button" className="inline-flex h-6 max-w-full items-center gap-1 rounded-[4px] bg-field px-1.5 font-mono text-[10.5px] text-ink-2 shadow-hairline transition-colors hover:bg-hover"><span className="truncate">{file.path}</span><span className="text-green">+{file.additions}</span>{file.deletions > 0 && <span className="text-red">−{file.deletions}</span>}</button>)}</div>}
    </div></div>
  </div>
}
