import { useMemo, useState } from 'react'
import type { FileSummary } from '../api/types'

type TreeNode = {
  name: string
  path: string
  children: TreeNode[]
  file?: FileSummary
}

function buildTree(files: FileSummary[]): TreeNode[] {
  const root: TreeNode = { name: '', path: '', children: [] }
  for (const file of files) {
    const parts = file.path.split('/').filter(Boolean)
    let parent = root
    parts.forEach((part, index) => {
      const path = parts.slice(0, index + 1).join('/')
      let node = parent.children.find((candidate) => candidate.name === part)
      if (!node) {
        node = { name: part, path, children: [] }
        parent.children.push(node)
      }
      if (index === parts.length - 1) node.file = file
      parent = node
    })
  }
  const sort = (nodes: TreeNode[]) => {
    nodes.sort((a, b) => Number(Boolean(a.file)) - Number(Boolean(b.file)) || a.name.localeCompare(b.name))
    nodes.forEach((node) => sort(node.children))
  }
  sort(root.children)
  return root.children
}

function FolderIcon({ open }: { open: boolean }) {
  return <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden><path d={open ? 'M3 7h6l2 2h10l-2 10H5L3 7Z' : 'M3 6h6l2 2h10v11H3V6Z'} /></svg>
}

function FileIcon() {
  return <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden><path d="M6 3h8l4 4v14H6zM14 3v5h5" /></svg>
}

function Branch({ node, depth, selectedPath, onSelect }: { node: TreeNode; depth: number; selectedPath?: string; onSelect: (path: string) => void }) {
  const directory = node.children.length > 0 && !node.file
  const selectedInside = selectedPath?.startsWith(`${node.path}/`) ?? false
  // Collapsed by default: `depth === 0` used to expand every root folder on open, which
  // buried the tree. Only the branch holding the shown file starts open.
  const [open, setOpen] = useState(selectedInside)
  if (directory) return <div>
    <button type="button" aria-expanded={open} onClick={() => setOpen((value) => !value)} className="flex h-7 w-full items-center gap-1.5 rounded-[3px] pr-2 text-left text-[11.5px] text-ink-2 transition-colors hover:bg-hover" style={{ paddingLeft: `${8 + depth * 14}px` }}>
      <svg width="10" height="10" viewBox="0 0 12 12" fill="none" className="shrink-0 text-ink-3 transition-transform duration-150" style={{ transform: open ? 'rotate(0deg)' : 'rotate(-90deg)' }} aria-hidden><path d="m3 4.5 3 3 3-3" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" strokeLinejoin="round" /></svg>
      <span className="shrink-0 text-ink-3"><FolderIcon open={open} /></span>
      <span className="truncate">{node.name}</span>
    </button>
    <div className={open ? 'block' : 'hidden'}>{node.children.map((child) => <Branch key={child.path} node={child} depth={depth + 1} selectedPath={selectedPath} onSelect={onSelect} />)}</div>
  </div>
  return <button type="button" onClick={() => onSelect(node.path)} aria-current={selectedPath === node.path ? 'page' : undefined} className={`flex h-7 w-full items-center gap-1.5 rounded-[3px] border pr-2 text-left font-mono text-[11px] transition-colors ${selectedPath === node.path ? 'border-line bg-hover-2 text-ink shadow-hairline' : 'border-transparent text-ink-3 hover:border-line hover:bg-hover hover:text-ink-2'}`} style={{ paddingLeft: `${28 + depth * 14}px` }}>
    <span className="shrink-0"><FileIcon /></span>
    <span className="min-w-0 flex-1 truncate">{node.name}</span>
    {node.file?.size != null && <span className="shrink-0 text-[9.5px] tabular-nums text-ink-3">{node.file.size}</span>}
  </button>
}

export function FileTree({ files, selectedPath, onSelect }: { files: FileSummary[]; selectedPath?: string; onSelect: (path: string) => void }) {
  const tree = useMemo(() => buildTree(files), [files])
  return <nav className="px-1.5 py-1" aria-label="Captured files">{tree.map((node) => <Branch key={node.path} node={node} depth={0} selectedPath={selectedPath} onSelect={onSelect} />)}</nav>
}
