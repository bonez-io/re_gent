import { useMemo, type CSSProperties } from 'react'
import { languageForPath, useCodeTokens, type ThemedToken } from '../lib/highlight'
import styles from './CodeView.module.css'

const FONT_STYLE_ITALIC = 1
const FONT_STYLE_BOLD = 2
const FONT_STYLE_UNDERLINE = 4

/** Oversized files stay inspectable without putting thousands of source rows in the DOM. */
export const LARGE_FILE_PREVIEW_LINES = 500

function tokenStyle(token: ThemedToken): CSSProperties | undefined {
  if (!token.htmlStyle) return undefined
  const style = { ...token.htmlStyle } as CSSProperties
  const fontStyle = token.fontStyle ?? 0
  if (fontStyle & FONT_STYLE_ITALIC) style.fontStyle = 'italic'
  if (fontStyle & FONT_STYLE_BOLD) style.fontWeight = 'bold'
  if (fontStyle & FONT_STYLE_UNDERLINE) style.textDecoration = 'underline'
  return style
}

export interface CodeLineProps {
  /** Tokens for this line, or `undefined` to render `text` unstyled (still monospace, still aligned). */
  tokens?: ThemedToken[]
  /** The raw source line, used verbatim when `tokens` isn't available and as a11y fallback otherwise. */
  text: string
  className?: string
}

/** Renders one source line as styled spans (or plain text) — independently usable so a blame view can compose its own grid around it. */
export function CodeLine({ tokens, text, className }: CodeLineProps) {
  const content = text.length === 0 ? ' ' : text
  if (!tokens || tokens.length === 0) return <span className={`${styles.line} ${className ?? ''}`}>{content}</span>
  return <span className={`${styles.line} ${className ?? ''}`}>{tokens.map((token, index) => <span key={index} className={styles.token} style={tokenStyle(token)}>{token.content}</span>)}</span>
}

export interface CodeViewProps {
  /** File path, used only to infer the shiki language — never rendered. */
  path: string
  code: string
  className?: string
}

/** Renders a whole file with line numbers, upgrading from plain text to syntax highlighting in place. Demo/reusable outside the blame view. */
export function CodeView({ path, code, className }: CodeViewProps) {
  const language = useMemo(() => languageForPath(path), [path])
  const { lines, state } = useCodeTokens(code, language)
  const rows = useMemo(() => (code.length === 0 ? [] : code.split('\n')), [code])
  const visibleRows = state === 'too-large' ? rows.slice(0, LARGE_FILE_PREVIEW_LINES) : rows
  const isTruncated = visibleRows.length < rows.length

  return <div className={`${styles.root} ${className ?? ''}`} data-state={state}>
    {state === 'empty' && <p className={styles.notice}>Empty file</p>}
    {state === 'binary' && <p className={styles.notice}>Binary file — preview not available</p>}
    {state === 'too-large' && <p className={styles.notice}>File too large to highlight ({rows.length.toLocaleString()} lines) — {isTruncated ? `showing first ${visibleRows.length.toLocaleString()} lines as plain text` : 'showing plain text'}</p>}
    {state !== 'empty' && state !== 'binary' && <pre className={styles.pre}>
      {visibleRows.map((row, index) => <div key={index} className={styles.row}>
        <span className={styles.gutter}>{index + 1}</span>
        <CodeLine tokens={lines?.[index]} text={row} />
      </div>)}
    </pre>}
  </div>
}
