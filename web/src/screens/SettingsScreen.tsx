export type SettingsSection = 'general' | 'users' | 'data'

const copy: Record<SettingsSection, { title: string; detail: string; rows: Array<[string, string]> }> = {
  general: { title: 'General', detail: 'Workspace behavior and personal preferences.', rows: [['Appearance', 'System default'], ['Default landing view', 'Sessions'], ['Notifications', 'Not configured']] },
  users: { title: 'Users', detail: 'Members, identities, and access will be managed here.', rows: [['Current user', 'Local user'], ['Workspace members', 'Not connected'], ['Roles and permissions', 'Coming later']] },
  data: { title: 'Data', detail: 'Retention, storage, export, and indexing controls.', rows: [['Storage', 'Local re_gent store'], ['Retention', 'Keep all captured history'], ['Semantic index', 'Not connected'], ['Export and backup', 'Coming later']] },
}

export function SettingsScreen({ section }: { section: SettingsSection }) {
  const content = copy[section]
  return <section className="min-h-0 flex-1 overflow-auto bg-page p-6 text-ink">
    <div className="mx-auto max-w-[760px]">
      <span className="regent-kicker">Settings</span>
      <h1 className="mb-0 mt-1 text-[20px] font-semibold tracking-[-0.02em]">{content.title}</h1>
      <p className="mb-5 mt-1 text-[12px] text-ink-3">{content.detail}</p>
      <div className="overflow-hidden rounded-[8px] border border-line bg-canvas shadow-hairline">
        {content.rows.map(([label, value]) => <div key={label} className="grid grid-cols-[190px_minmax(0,1fr)] items-center border-b border-line px-4 py-3 text-[12px] last:border-0 max-sm:grid-cols-1 max-sm:gap-1"><span className="font-medium text-ink-2">{label}</span><span className="text-ink-3">{value}</span></div>)}
      </div>
      <p className="mt-3 text-[10.5px] text-ink-3">This foundation is intentionally read-only until the settings APIs are wired.</p>
    </div>
  </section>
}
