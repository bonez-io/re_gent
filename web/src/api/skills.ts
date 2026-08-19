/**
 * The skill catalog.
 *
 * A skill is a prompt plus a tool grant: the intelligence is the agent's, the
 * data is re_gent's, and the skill is the wiring between them. Each entry here
 * mirrors a `SKILL.md` under `.claude/skills/`.
 *
 * The catalog is bundled rather than fetched. Skills only *work* as files on
 * disk that an agent loads at startup, so the repository is their source of
 * truth today. When the server grows a skills registry, this module is the
 * seam: `listSkills` becomes a fetch and nothing else in the UI changes.
 */

/** Which captured data a skill reads. Drives the facets in the Skills view. */
export type SkillSource = 'blame' | 'steps' | 'conversations' | 'files' | 'usage'

export type SkillCategory = 'provenance' | 'structure' | 'conversation' | 'meta'

export type Skill = {
  name: string
  title: string
  /** The `description` line from SKILL.md — what the agent matches against. */
  description: string
  category: SkillCategory
  /** Captured data the skill reads. */
  sources: SkillSource[]
  /** Commands granted in `allowed-tools`. */
  commands: string[]
  argumentHint?: string
  /** Present on disk in this repository, so an agent can run it now. */
  installed: boolean
  /** Answers a question that needs re_gent's prompt-to-line link — not Git's. */
  regentOnly: boolean
  /** Shown on the detail panel as a concrete way to invoke it. */
  example: string
}

export const skills: Skill[] = [
  {
    name: 'bug-blame',
    title: 'Bug blame',
    description: 'Trace a bug or incident back through captured history to the change that caused it, the prompt behind that change, and its blast radius.',
    category: 'provenance',
    sources: ['blame', 'steps', 'conversations'],
    commands: ['rgt blame', 'rgt log', 'rgt show', 'rgt sessions'],
    argumentHint: '<path>[:<line>] or a description of the symptom',
    installed: true,
    regentOnly: true,
    example: 'bug-blame src/billing.js:2',
  },
  {
    name: 'context-primer',
    title: 'Context primer',
    description: 'Load everything captured history knows about a file or area before work starts, so the agent begins warm instead of from zero.',
    category: 'conversation',
    sources: ['blame', 'steps', 'conversations'],
    commands: ['rgt blame', 'rgt log', 'rgt show', 'rgt sessions'],
    argumentHint: '<path> or <area description>',
    installed: true,
    regentOnly: true,
    example: 'context-primer src/billing.js',
  },
  {
    name: 'file-coupling',
    title: 'File coupling',
    description: 'Find files that habitually change together, from captured step history.',
    category: 'structure',
    sources: ['steps', 'files'],
    commands: ['rgt log', 'rgt sessions'],
    argumentHint: '[path] [--sessions N]',
    installed: true,
    regentOnly: false,
    example: 'file-coupling src/billing.js',
  },
  {
    name: 'style-factory',
    title: 'Style factory',
    description: 'Read captured conversations to learn how this person actually works, then propose skills tailored to them.',
    category: 'meta',
    sources: ['conversations', 'steps'],
    commands: ['rgt log', 'rgt sessions', 'rgt show'],
    argumentHint: '[--sessions N]',
    installed: true,
    regentOnly: true,
    example: 'style-factory --sessions 30',
  },
  {
    name: 'skill-factory',
    title: 'Skill factory',
    description: 'Create a new re_gent skill from a plain-language description, writing a valid SKILL.md.',
    category: 'meta',
    sources: ['steps'],
    commands: ['rgt log', 'rgt sessions'],
    argumentHint: '<description of what the skill should do>',
    installed: true,
    regentOnly: false,
    example: 'skill-factory "find tests that changed without their source"',
  },
  {
    name: 'blame',
    title: 'Blame',
    description: 'Show which re_gent step last modified each line of a file.',
    category: 'provenance',
    sources: ['blame'],
    commands: ['rgt blame'],
    argumentHint: '<path>[:<line>]',
    installed: true,
    regentOnly: true,
    example: 'blame src/invoice.js:1',
  },
  {
    name: 'log',
    title: 'Log',
    description: 'View the re_gent activity log for the default or selected session.',
    category: 'structure',
    sources: ['steps', 'conversations'],
    commands: ['rgt log'],
    installed: true,
    regentOnly: false,
    example: 'log --session sess-billing-01',
  },
  {
    name: 'show',
    title: 'Show',
    description: 'Show detailed context for a re_gent step, including tool calls, tool results, and conversation.',
    category: 'provenance',
    sources: ['steps', 'conversations'],
    commands: ['rgt show'],
    argumentHint: '<step-hash>',
    installed: true,
    regentOnly: true,
    example: 'show b7ab3e66',
  },
  {
    name: 'rewind',
    title: 'Rewind',
    description: 'Restore the workspace to the tree recorded at an earlier step.',
    category: 'structure',
    sources: ['steps', 'files'],
    commands: ['rgt rewind'],
    argumentHint: '<step-hash>',
    installed: true,
    regentOnly: false,
    example: 'rewind b7ab3e66 --dry-run',
  },
  // Proposed, not yet written. Listed so the catalog shows where this is going
  // and so `installed` has a meaningful false case before a registry exists.
  {
    name: 'token-audit',
    title: 'Token audit',
    description: 'Report which sessions and tasks consumed the most tokens, using the per-step usage re_gent records.',
    category: 'meta',
    sources: ['usage', 'steps'],
    commands: ['rgt log', 'rgt sessions'],
    installed: false,
    regentOnly: true,
    example: 'token-audit --sessions 20',
  },
  {
    name: 'pr-narrative',
    title: 'PR narrative',
    description: 'Write a pull-request description from the steps and prompts that actually produced the branch.',
    category: 'conversation',
    sources: ['steps', 'conversations'],
    commands: ['rgt log', 'rgt show'],
    installed: false,
    regentOnly: true,
    example: 'pr-narrative --session sess-billing-01',
  },
  {
    name: 'regression-hunter',
    title: 'Regression hunter',
    description: 'For code that used to work, find the last step that touched it and the prompt that drove the change.',
    category: 'provenance',
    sources: ['blame', 'steps', 'conversations'],
    commands: ['rgt blame', 'rgt log', 'rgt show'],
    installed: false,
    regentOnly: true,
    example: 'regression-hunter tests/billing.test.js',
  },
]

export const categoryLabels: Record<SkillCategory, string> = {
  provenance: 'Provenance',
  structure: 'Structure',
  conversation: 'Conversation',
  meta: 'Meta',
}

/** The seam a server-side registry would replace. */
export function listSkills(): Skill[] {
  return skills
}

/**
 * The base URL a generated prompt should fetch skills from.
 *
 * The browser talks to the server through Vite's proxy in development, so the
 * page origin is not the server address. An explicit `VITE_REGENT_SERVER_URL`
 * wins; otherwise a production build is served from the same origin as its
 * server, and development falls back to the default local server.
 */
export function serverBaseUrl(): string {
  const configured = import.meta.env.VITE_REGENT_SERVER_URL as string | undefined
  if (configured) return configured.replace(/\/+$/, '')
  if (import.meta.env.PROD && typeof window !== 'undefined') return window.location.origin
  return 'http://127.0.0.1:7654'
}

/**
 * Build the prompt a user pastes into their agent to install skills.
 *
 * The UI deliberately does not write to disk: it produces text, and the agent
 * does the installing. That keeps the first UI epic read-only (RFC 0001) and
 * makes the flow work in any harness that can fetch and write a file, rather
 * than only in the one we happened to code for.
 *
 * The prompt asks the agent to surface each skill's tool grant before writing.
 * A SKILL.md is not inert documentation — `allowed-tools` decides what the
 * skill may run — so the grant belongs in front of the person approving it.
 */
export function installPrompt(selected: Skill[], baseUrl = serverBaseUrl()): string {
  if (selected.length === 0) return ''
  const names = selected.map((skill) => skill.name)
  const plural = names.length === 1 ? 'skill' : 'skills'

  return [
    `Run this to install ${names.length} re_gent ${plural}:`,
    '',
    `    ${installCommand(selected)}`,
    '',
    'It prints what each skill is allowed to run as it installs, and never replaces',
    'a skill file you have edited without --force. Restart this session afterwards —',
    'skills load at startup.',
    '',
    `If rgt is not installed, fetch each definition from ${baseUrl}/api/skills/<name>`,
    'and write it to .claude/skills/<name>/SKILL.md instead.',
  ].join('\n')
}

/**
 * The one-liner the copy button is really for.
 *
 * `rgt skill install` does the work — resolving the host's skill directory,
 * refusing to clobber an edited file, and printing each tool grant — so the
 * thing a user pastes is a command, not a paragraph of instructions.
 */
export function installCommand(selected: Skill[]): string {
  if (selected.length === 0) return ''
  return `rgt skill install ${selected.map((skill) => skill.name).join(' ')}`
}
