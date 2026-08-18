import { useState } from 'react'

export interface CodeBlockProps { filename: string; language: string; code: string }

/** Adapted from Beautiful UI's MIT-licensed Code Block primitive. */
export function CodeBlock({ filename, language, code }: CodeBlockProps) {
  const [copied, setCopied] = useState(false)
  const copy = async () => { await navigator.clipboard.writeText(code); setCopied(true); window.setTimeout(() => setCopied(false), 1500) }
  return <div className="w-full overflow-hidden rounded-[7px] border border-line bg-inset transition-colors duration-150 hover:border-ink-3/40">
    <div className="flex min-h-7 items-center justify-between border-b border-line px-2">
      <span className="flex items-baseline gap-2"><span className="font-mono text-[11.5px] font-medium text-ink">{filename}</span><span className="text-[10.5px] text-ink-3">{language}</span></span>
      <button aria-label="Copy code" onClick={copy} className={`flex h-6 items-center gap-1 rounded-[4px] px-1.5 text-[10.5px] font-medium transition-colors hover:bg-hover ${copied ? 'text-green' : 'text-ink-3 hover:text-ink'}`}>{copied ? 'Copied' : 'Copy'}</button>
    </div>
    <pre className="m-0 overflow-auto bg-inset py-1.5 font-mono text-[11.5px] leading-[1.55]">{code.split('\n').map((line, index) => <div key={`${index}-${line}`} className="flex px-2"><span className="w-6 shrink-0 select-none text-right text-[10px] text-ink-3/60">{index + 1}</span><code className="pl-2.5 text-ink-2">{line || ' '}</code></div>)}</pre>
  </div>
}
