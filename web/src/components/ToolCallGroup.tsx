import { useEffect, useState } from 'react'

export type ToolCall = { id: string; tool: string; summary: string; detail?: string[]; status?: 'complete' | 'failed' }
export type FileChange = { path: string; additions: number; deletions: number }
export interface ToolCallGroupProps { calls: ToolCall[]; files?: FileChange[]; defaultOpen?: boolean; allOpen?: boolean }

function ToolIcon({ tool, failed }: { tool: string; failed?: boolean }) {
  const path = tool === 'Read' ? <><path d="M4 19.5V5.75A2.75 2.75 0 0 1 6.75 3H20v15H6.75A2.75 2.75 0 0 0 4 20.75" /><path d="M8 7h8M8 10h6" /></> : tool === 'Search' ? <><circle cx="10.5" cy="10.5" r="6" /><path d="m15 15 5 5" /></> : tool === 'Edit' ? <><path d="m4 20 4.5-1 10-10-3.5-3.5-10 10zM13.5 7l3.5 3.5" /></> : <><path d="M4 17l6-5-6-5M12 19h8" /></>
  return <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" className={failed ? 'text-red' : 'text-ink-3'}>{path}</svg>
}

function actionLabel(calls: ToolCall[]) {
  const failed = calls.some((call) => call.status === 'failed')
  if (failed) return calls.length === 1 ? 'Tool failed' : 'Tool calls failed'
  const names = new Set(calls.map((call) => call.tool))
  if (names.size === 1) {
    const [tool] = [...names]
    const count = new Set(calls.map((call) => call.summary)).size || calls.length
    if (tool === 'Read') return count === 1 ? 'Read files' : `Read ${count} files`
    if (tool === 'Edit') return count === 1 ? 'Edited a file' : `Edited ${count} files`
    if (tool === 'Search') return 'Searched files'
    if (tool === 'Bash') return count === 1 ? 'Ran command' : `Ran ${count} commands`
  }
  if ([...names].every((name) => ['Read', 'Search'].includes(name))) return 'Read files'
  return calls.length === 1 ? `Used ${calls[0].tool}` : 'Used tools'
}

function groupIconTool(calls: ToolCall[]) {
  const names = calls.map((call) => call.tool)
  if (names.includes('Edit')) return 'Edit'
  if (names.includes('Bash')) return 'Bash'
  if (names.includes('Search')) return 'Search'
  return names[0] || 'Tool'
}

/** Adapted from Beautiful UI's MIT-licensed Tool Chips primitive. */
export function ToolCallGroup({ calls, files = [], defaultOpen = false, allOpen }: ToolCallGroupProps) {
  const [open, setOpen] = useState(defaultOpen)
  useEffect(() => {
    if (allOpen !== undefined) setOpen(allOpen)
  }, [allOpen])
  const failed = calls.some((call) => call.status === 'failed')
  return <div className="w-full max-w-[920px]">
    <button type="button" aria-expanded={open} onClick={() => setOpen((value) => !value)} className="-mx-1 flex w-fit items-center gap-3 rounded-[4px] px-1 py-0.5 text-[15px] text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink">
      <ToolIcon tool={groupIconTool(calls)} failed={failed} />
      <span>{actionLabel(calls)}</span>
      <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.1" className="text-ink-3 transition-transform duration-200" style={{ transform: open ? 'rotate(0deg)' : 'rotate(-90deg)' }}><path d="M6 9l6 6 6-6" /></svg>
    </button>
    <div className="grid transition-[grid-template-rows,opacity] duration-300" style={{ gridTemplateRows: open ? '1fr' : '0fr', opacity: open ? 1 : 0 }}><div className="min-h-0 overflow-hidden">
      <div className="mt-2 ml-7 flex flex-col gap-2 border-l border-line pl-4">
        {calls.map((call) => <div key={call.id} className="min-w-0">
          <div className="flex min-w-0 items-baseline gap-3 text-[12px]">
            <span className="shrink-0 font-medium text-ink-2">{call.tool}</span>
            <span className="min-w-0 truncate font-mono text-[11.5px] text-ink-3">{call.summary}</span>
          </div>
          {call.detail && call.detail.length > 0 && <div className="mt-1 flex flex-col font-mono text-[11px] leading-[1.55] text-ink-3">
            {call.detail.map((line) => <span key={line} className={call.status === 'failed' ? 'text-red' : ''}>{line}</span>)}
          </div>}
        </div>)}
      </div>
      {files.length > 0 && <div className="mt-2 ml-7 flex flex-wrap gap-1.5">{files.map((file) => <button key={file.path} type="button" className="inline-flex h-6 max-w-full items-center gap-1 rounded-[3px] border border-line bg-field px-1.5 font-mono text-[10.5px] text-ink-2 shadow-hairline transition-colors hover:bg-hover"><span className="truncate">{file.path}</span><span className="text-green">+{file.additions}</span>{file.deletions > 0 && <span className="text-red">−{file.deletions}</span>}</button>)}</div>}
    </div></div>
  </div>
}
