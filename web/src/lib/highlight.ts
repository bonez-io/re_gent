import { useEffect, useMemo, useState } from 'react'
import type { HighlighterCore, LanguageRegistration, ThemedToken } from 'shiki/core'

export type { ThemedToken }

/** Language id used for anything we don't recognize, or don't want to tokenize. */
export const PLAIN_LANGUAGE = 'text'

// The high-contrast pair, chosen by measurement rather than taste: against this
// app's backgrounds (#101010 dark, #f4f4f1 light) they are the only GitHub themes
// whose common code scopes clear WCAG AA 4.5:1 — 8.97:1 and 4.57:1. Plain
// github-dark renders comments at 3.95:1 and fails.
const THEME_DARK = 'github-dark-high-contrast'
const THEME_LIGHT = 'github-light-high-contrast'

/** Above this many lines, skip highlighting and render plain text instead. */
const MAX_LINES = 5000
/** Above this many bytes, skip highlighting and render plain text instead. */
const MAX_BYTES = 512 * 1024
/** How much of a file to sample when sniffing for binary content. */
const BINARY_SNIFF_LENGTH = 4096
/** Share of non-printable characters in the sample above which a file is treated as binary. */
const BINARY_NON_PRINTABLE_RATIO = 0.3

const NAMED_LANGUAGES: Record<string, string> = {
  dockerfile: 'dockerfile',
  makefile: 'makefile',
}

const EXTENSION_LANGUAGES: Record<string, string> = {
  ts: 'typescript', mts: 'typescript', cts: 'typescript',
  tsx: 'tsx',
  js: 'javascript', mjs: 'javascript', cjs: 'javascript',
  jsx: 'jsx',
  json: 'json',
  jsonc: 'jsonc',
  go: 'go',
  md: 'markdown', markdown: 'markdown',
  yaml: 'yaml', yml: 'yaml',
  toml: 'toml',
  css: 'css',
  html: 'html', htm: 'html',
  sh: 'bash', bash: 'bash', zsh: 'zsh',
  py: 'python',
  rs: 'rust',
  sql: 'sql',
  java: 'java',
  rb: 'ruby',
  php: 'php',
  c: 'c', h: 'c',
  cpp: 'cpp', cc: 'cpp', cxx: 'cpp', hpp: 'cpp', hh: 'cpp',
  swift: 'swift',
  kt: 'kotlin', kts: 'kotlin',
  env: 'dotenv',
}

/**
 * Dynamic loaders for shiki's fine-grained per-language grammars, keyed by the
 * language id `languageForPath` returns. Each entry is a separate `import()`
 * so bundlers can code-split every language into its own lazily-loaded chunk —
 * loading the full `shiki` bundle would pull in all ~346 languages at once.
 */
const LANGUAGE_LOADERS: Record<string, () => Promise<{ default: LanguageRegistration[] }>> = {
  typescript: () => import('shiki/langs/typescript.mjs'),
  tsx: () => import('shiki/langs/tsx.mjs'),
  javascript: () => import('shiki/langs/javascript.mjs'),
  jsx: () => import('shiki/langs/jsx.mjs'),
  json: () => import('shiki/langs/json.mjs'),
  jsonc: () => import('shiki/langs/jsonc.mjs'),
  go: () => import('shiki/langs/go.mjs'),
  markdown: () => import('shiki/langs/markdown.mjs'),
  yaml: () => import('shiki/langs/yaml.mjs'),
  toml: () => import('shiki/langs/toml.mjs'),
  css: () => import('shiki/langs/css.mjs'),
  html: () => import('shiki/langs/html.mjs'),
  bash: () => import('shiki/langs/bash.mjs'),
  zsh: () => import('shiki/langs/zsh.mjs'),
  python: () => import('shiki/langs/python.mjs'),
  rust: () => import('shiki/langs/rust.mjs'),
  sql: () => import('shiki/langs/sql.mjs'),
  java: () => import('shiki/langs/java.mjs'),
  ruby: () => import('shiki/langs/ruby.mjs'),
  php: () => import('shiki/langs/php.mjs'),
  c: () => import('shiki/langs/c.mjs'),
  cpp: () => import('shiki/langs/cpp.mjs'),
  swift: () => import('shiki/langs/swift.mjs'),
  kotlin: () => import('shiki/langs/kotlin.mjs'),
  dockerfile: () => import('shiki/langs/dockerfile.mjs'),
  makefile: () => import('shiki/langs/makefile.mjs'),
  dotenv: () => import('shiki/langs/dotenv.mjs'),
}

/** Maps a file path to a shiki language id. Falls back to `PLAIN_LANGUAGE`, never throws. */
export function languageForPath(path: string): string {
  try {
    if (!path) return PLAIN_LANGUAGE
    const base = (path.split('/').pop() || path).toLowerCase()
    const named = NAMED_LANGUAGES[base]
    if (named) return named
    const dot = base.lastIndexOf('.')
    const ext = dot >= 0 ? base.slice(dot + 1) : ''
    return EXTENSION_LANGUAGES[ext] ?? PLAIN_LANGUAGE
  } catch {
    return PLAIN_LANGUAGE
  }
}

let highlighterPromise: Promise<HighlighterCore> | null = null

/** Module-level singleton highlighter. Themes load once; languages load lazily and are cached. */
function getHighlighter(): Promise<HighlighterCore> {
  if (!highlighterPromise) {
    // shiki's core and regex engine are imported dynamically, not just its grammars:
    // statically importing them put ~61KB gzipped into the initial bundle for a
    // feature only the file view uses.
    highlighterPromise = (async () => {
      const [{ createHighlighterCore }, { createJavaScriptRegexEngine }] = await Promise.all([
        import('shiki/core'),
        import('shiki/engine/javascript'),
      ])
      return createHighlighterCore({
        themes: [import('shiki/themes/github-dark-high-contrast.mjs'), import('shiki/themes/github-light-high-contrast.mjs')],
        langs: [],
        engine: createJavaScriptRegexEngine(),
      })
    })()
  }
  return highlighterPromise
}

const languageLoadPromises = new Map<string, Promise<void>>()

/** Loads a language grammar into the shared highlighter at most once, caching the in-flight promise. */
function ensureLanguage(highlighter: HighlighterCore, language: string): Promise<void> {
  const loader = LANGUAGE_LOADERS[language]
  if (!loader) return Promise.resolve()
  let promise = languageLoadPromises.get(language)
  if (!promise) {
    promise = highlighter.loadLanguage(loader())
    languageLoadPromises.set(language, promise)
  }
  return promise
}

export type HighlightState = 'loading' | 'highlighted' | 'plain' | 'binary' | 'too-large' | 'empty'

export interface CodeTokensResult {
  /** One token array per source line. `undefined` until highlighting resolves (or never, if it won't). */
  lines: ThemedToken[][] | undefined
  state: HighlightState
  /** Human-readable explanation for `binary` / `too-large`, for the UI to display. */
  reason?: string
}

function isLikelyBinary(code: string): boolean {
  if (code.indexOf('\u0000') !== -1) return true
  const sampleLength = Math.min(code.length, BINARY_SNIFF_LENGTH)
  if (sampleLength === 0) return false
  let nonPrintable = 0
  for (let i = 0; i < sampleLength; i++) {
    const charCode = code.charCodeAt(i)
    if (charCode === 9 || charCode === 10 || charCode === 13) continue // tab, LF, CR
    if (charCode < 32 || charCode === 127) nonPrintable++
  }
  return nonPrintable / sampleLength > BINARY_NON_PRINTABLE_RATIO
}

function countLines(code: string): number {
  let count = 1
  for (let i = 0; i < code.length; i++) if (code.charCodeAt(i) === 10) count++
  return count
}

function byteLength(code: string): number {
  try {
    return new TextEncoder().encode(code).length
  } catch {
    return code.length
  }
}

/** Synchronous classification: empty / binary / too-large / unknown-language cases never touch the highlighter. */
function classify(code: string, language: string): CodeTokensResult {
  if (code.length === 0) return { lines: undefined, state: 'empty' }
  if (isLikelyBinary(code)) return { lines: undefined, state: 'binary', reason: 'Binary content detected' }
  const lineCount = countLines(code)
  if (lineCount > MAX_LINES) return { lines: undefined, state: 'too-large', reason: `${lineCount.toLocaleString()} lines` }
  const bytes = byteLength(code)
  if (bytes > MAX_BYTES) return { lines: undefined, state: 'too-large', reason: `${Math.round(bytes / 1024).toLocaleString()} KB` }
  if (language === PLAIN_LANGUAGE || !LANGUAGE_LOADERS[language]) return { lines: undefined, state: 'plain' }
  return { lines: undefined, state: 'loading' }
}

async function highlightCode(code: string, language: string): Promise<ThemedToken[][]> {
  const highlighter = await getHighlighter()
  await ensureLanguage(highlighter, language)
  const { tokens } = await highlighter.codeToTokens(code, {
    lang: language,
    themes: { light: THEME_LIGHT, dark: THEME_DARK },
    defaultColor: false,
  })
  return tokens
}

type AsyncOutcome = { status: 'pending' } | { status: 'done'; lines: ThemedToken[][] } | { status: 'failed' }

/**
 * Async, non-blocking, non-suspending code highlighter hook.
 * Renders plain text immediately (via `state`) and upgrades to `highlighted` once tokens resolve.
 * Ignores results that resolve after `code`/`language` have changed or the component unmounted.
 */
export function useCodeTokens(code: string, language: string): CodeTokensResult {
  const classification = useMemo(() => classify(code, language), [code, language])
  const [asyncOutcome, setAsyncOutcome] = useState<AsyncOutcome>({ status: 'pending' })

  useEffect(() => {
    if (classification.state !== 'loading') return
    let cancelled = false
    setAsyncOutcome({ status: 'pending' })
    highlightCode(code, language).then(
      (lines) => { if (!cancelled) setAsyncOutcome({ status: 'done', lines }) },
      () => { if (!cancelled) setAsyncOutcome({ status: 'failed' }) },
    )
    return () => { cancelled = true }
  }, [code, language, classification.state])

  if (classification.state !== 'loading') return classification
  if (asyncOutcome.status === 'done') return { lines: asyncOutcome.lines, state: 'highlighted' }
  if (asyncOutcome.status === 'failed') return { lines: undefined, state: 'plain' }
  return { lines: undefined, state: 'loading' }
}
