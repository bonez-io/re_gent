import { useState } from 'react'

export interface CodeBlockProps { filename: string; language: string; code: string }

/** Adapted from Beautiful UI's MIT-licensed Code Block primitive. */
export function CodeBlock({ filename, language, code }: CodeBlockProps) {
  const [copied, setCopied] = useState(false)
  const copy = async () => { await navigator.clipboard.writeText(code); setCopied(true); window.setTimeout(() => setCopied(false), 1500) }
  return <div className="regent-terminal w-full overflow-hidden rounded-[4px] transition-colors duration-150">
    <div className="flex min-h-7 items-center justify-between border-b border-white/12 px-2">
      <span className="flex items-baseline gap-2"><span className="font-mono text-[11.5px] font-medium text-terminal-ink">{filename}</span><span className="text-[10.5px] text-terminal-muted">{language}</span></span>
      <button aria-label="Copy code" onClick={copy} className={`flex h-6 items-center gap-1 rounded-[2px] px-1.5 text-[10.5px] font-medium transition-colors hover:bg-white/10 ${copied ? 'text-green' : 'text-terminal-muted hover:text-terminal-ink'}`}>{copied ? 'Copied' : 'Copy'}</button>
    </div>
    <pre className="m-0 overflow-auto py-1.5 font-mono text-[11.5px] leading-[1.55]">{code.split('\n').map((line, index) => <div key={`${index}-${line}`} className="flex px-2"><span className="w-6 shrink-0 select-none text-right text-[10px] text-terminal-muted/70">{index + 1}</span><code className="pl-2.5 text-terminal-ink">{line || ' '}</code></div>)}</pre>
  </div>
}
