import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { FileDiffView } from './FileDiffView'

export type ToolCall = { id: string; tool: string; summary: string; detail?: string[]; status?: 'complete' | 'failed' }
export type FileChange = { path: string; additions: number; deletions: number }
export interface ToolCallGroupProps { calls: ToolCall[]; files?: FileChange[]; defaultOpen?: boolean; allOpen?: boolean; repoId?: string; stepHash?: string }

const TOOL_DETAIL_PREVIEW_CHARS = 4000

function ToolIcon({ tool, failed }: { tool: string; failed?: boolean }) {
  const path = tool === 'Read' ? <><path d="M4 19.5V5.75A2.75 2.75 0 0 1 6.75 3H20v15H6.75A2.75 2.75 0 0 0 4 20.75" /><path d="M8 7h8M8 10h6" /></> : tool === 'Search' ? <><circle cx="10.5" cy="10.5" r="6" /><path d="m15 15 5 5" /></> : tool === 'Edit' ? <><path d="m4 20 4.5-1 10-10-3.5-3.5-10 10zM13.5 7l3.5 3.5" /></> : <><path d="M4 17l6-5-6-5M12 19h8" /></>
  return <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" className={failed ? 'text-red' : 'text-ink-3'}>{path}</svg>
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

/** Quiet status box matching FileDiffView's own frame, for the states that never reach
 *  a rendered diff (no context to fetch with, still loading, failed, or no entry for this
 *  path) — so an expanded chip never leaves a blank gap. */
function DiffStatus({ children }: { children: React.ReactNode }) {
  return <div className="w-full max-w-[920px] rounded-[4px] border border-line bg-canvas px-2.5 py-2 text-[11.5px] text-ink-3 shadow-hairline">{children}</div>
}

/** Tool results can contain screenshots or other encoded payloads on a single enormous line.
 *  Keep the transcript useful while the complete result remains available from step details. */
function ToolDetail({ lines, failed }: { lines: string[]; failed?: boolean }) {
  const text = lines.join('\n')
  const truncated = text.length > TOOL_DETAIL_PREVIEW_CHARS
  const shown = truncated ? text.slice(0, TOOL_DETAIL_PREVIEW_CHARS) : text
  return <div className="mt-0.5">
    <pre tabIndex={0} aria-label="Tool result preview" className={`m-0 max-h-[220px] overflow-auto whitespace-pre-wrap break-all font-mono text-[10px] leading-[1.4] ${failed ? 'text-red' : 'text-ink-3'}`}>{shown}{truncated ? '…' : ''}</pre>
    {truncated && <p className="m-0 mt-1 text-[10px] text-ink-3">Showing first {TOOL_DETAIL_PREVIEW_CHARS.toLocaleString()} of {text.length.toLocaleString()} characters. Open the step for the complete result.</p>}
  </div>
}

/** Adapted from Beautiful UI's MIT-licensed Tool Chips primitive. */
export function ToolCallGroup({ calls, files = [], defaultOpen = false, allOpen, repoId, stepHash }: ToolCallGroupProps) {
  const [open, setOpen] = useState(defaultOpen)
  useEffect(() => {
    if (allOpen !== undefined) setOpen(allOpen)
  }, [allOpen])
  const failed = calls.some((call) => call.status === 'failed')

  // Which chips the reader has expanded. Every open chip in this step shares one request
  // (same queryKey), so opening a second file next to an already-open one costs nothing extra.
  const [openPaths, setOpenPaths] = useState<Set<string>>(() => new Set())
  const toggleFile = (path: string) => setOpenPaths((prev) => {
    const next = new Set(prev)
    if (next.has(path)) next.delete(path)
    else next.add(path)
    return next
  })

  // A first step has no parent, so its diff is the whole tree as additions — hundreds of KB
  // for a real repo. Never fetch just because the group rendered; only once a chip is open,
  // and only when there is a step to diff against.
  const diffQuery = useQuery({
    queryKey: ['diff', repoId, stepHash],
    queryFn: () => api.diff(repoId!, stepHash!),
    enabled: openPaths.size > 0 && Boolean(repoId) && Boolean(stepHash),
  })

  return <div className="w-full max-w-[920px]">
    <button type="button" aria-expanded={open} onClick={() => setOpen((value) => !value)} className="-mx-1 flex h-6 w-fit items-center gap-1.5 rounded-[4px] px-1 text-[11.5px] text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink">
      <ToolIcon tool={groupIconTool(calls)} failed={failed} />
      <span>{actionLabel(calls)}</span>
      <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.3" className="text-ink-3 transition-transform duration-200" style={{ transform: open ? 'rotate(0deg)' : 'rotate(-90deg)' }}><path d="M6 9l6 6 6-6" /></svg>
    </button>
    <div className="grid transition-[grid-template-rows,opacity] duration-300" style={{ gridTemplateRows: open ? '1fr' : '0fr', opacity: open ? 1 : 0 }}><div className="min-h-0 overflow-hidden">
      {open && <><div className="mt-1.5 ml-5 flex flex-col gap-1 border-l border-line pl-3">
        {calls.map((call) => <div key={call.id} className="min-w-0">
          <div className="flex min-w-0 items-baseline gap-2 text-[11.5px]">
            <span className="shrink-0 font-medium text-ink-2">{call.tool}</span>
            <span className="min-w-0 truncate font-mono text-[10.5px] text-ink-3">{call.summary}</span>
          </div>
          {call.detail && call.detail.length > 0 && <ToolDetail lines={call.detail} failed={call.status === 'failed'} />}
        </div>)}
      </div>
      {files.length > 0 && <div className="mt-1.5 ml-5 flex flex-col gap-1.5">
        <div className="flex flex-wrap gap-1">{files.map((file) => {
          // The adapter that builds `files` hardcodes additions/deletions to 0 — showing them
          // would be a lie. Real counts only exist once the step diff has loaded, and only for
          // paths it actually touched (a file can be read without being modified).
          const matched = diffQuery.data?.files.find((entry) => entry.path === file.path)
          const isOpen = openPaths.has(file.path)
          return <button key={file.path} type="button" aria-expanded={isOpen} onClick={() => toggleFile(file.path)}
            className="inline-flex h-6 max-w-full items-center gap-1 rounded-[3px] border border-line bg-field px-1 font-mono text-[10px] text-ink-2 shadow-hairline transition-colors hover:bg-hover">
            <span className="truncate">{file.path}</span>
            {matched && <span className="text-green">+{matched.additions}</span>}
            {matched && matched.deletions > 0 && <span className="text-red">−{matched.deletions}</span>}
          </button>
        })}</div>
        {files.filter((file) => openPaths.has(file.path)).map((file) => {
          const href = repoId && stepHash ? `/repos/${encodeURIComponent(repoId)}/files?step=${encodeURIComponent(stepHash)}&path=${encodeURIComponent(file.path)}` : undefined
          const matched = diffQuery.data?.files.find((entry) => entry.path === file.path)
          return <div key={file.path}>
            {!repoId || !stepHash
              ? <DiffStatus>Diff unavailable for this tool call.</DiffStatus>
              : diffQuery.isPending
                ? <DiffStatus>Loading diff…</DiffStatus>
                : diffQuery.isError
                  ? <DiffStatus>Couldn't load the diff{diffQuery.error instanceof Error ? `: ${diffQuery.error.message}` : ''}.</DiffStatus>
                  : matched
                    ? <FileDiffView diff={matched} href={href} />
                    : <DiffStatus>No changes recorded for this file.</DiffStatus>}
          </div>
        })}
      </div>}</>}
    </div></div>
  </div>
}
