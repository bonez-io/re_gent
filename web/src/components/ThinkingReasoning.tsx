import { useState } from 'react'
import styles from './ThinkingReasoning.module.css'

export interface ThinkingReasoningProps { lines: string[]; durationSeconds?: number; defaultOpen?: boolean; thinking?: boolean }

/** Adapted from AI CSS's free Thinking + Reasoning React component. */
export function ThinkingReasoning({ lines, durationSeconds = 12, defaultOpen = false, thinking = false }: ThinkingReasoningProps) {
  const [open, setOpen] = useState(defaultOpen || thinking)
  const expanded = thinking || open
  return <div className={styles.root}>
    <button type="button" className={styles.header} aria-expanded={expanded} aria-label="Toggle reasoning" onClick={() => !thinking && setOpen((value) => !value)}>
      <span className={`${styles.label} ${thinking ? styles.shimmer : ''}`}>{thinking ? 'Thinking…' : <><span className={styles.verb}>Thought</span> for {durationSeconds}s</>}</span>
      {!thinking && <svg className={styles.chevron} viewBox="0 0 24 24" width="12" height="12" aria-hidden="true"><path d="m4.5 15.75 7.5-7.5 7.5 7.5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" /></svg>}
    </button>
    <div className={`${styles.collapsible} ${expanded ? '' : styles.collapsed}`}><div className={styles.inner}><div className={styles.content}>{lines.map((line) => <p key={line}>{line}</p>)}</div></div></div>
  </div>
}
