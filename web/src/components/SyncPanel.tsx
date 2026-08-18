export interface SyncPanelProps { state?: 'connected' | 'stale' | 'offline' }

export function SyncPanel({ state = 'connected' }: SyncPanelProps) {
  const status = state === 'connected' ? ['Connected', 'text-green', 'bg-green'] : state === 'stale' ? ['Behind by 3 steps', 'text-ink-2', 'bg-accent'] : ['Offline', 'text-red', 'bg-red']
  const rows = [
    ['Server', 'http://127.0.0.1:7654'], ['Repository', 'girlfriend-assistant'], ['Remote ID', 'repo_01JZQ6TGNNY4'], ['Session refs', '7'], ['Objects', '1,284 · 8.7 MB'], ['Last push', state === 'offline' ? 'Failed · 6m ago' : '2 steps · 11s ago'], ['Last pull', 'Up to date · 14s ago'],
  ]
  return <section className="mx-auto w-full max-w-[720px] p-5">
    <div className="mb-5 flex items-start justify-between"><div><h2 className="m-0 text-[14px] font-semibold">Sync status</h2><p className="m-0 mt-1 text-[10.5px] text-ink-3">Object store and session refs for this repository.</p></div><span className={`flex items-center gap-1.5 text-[10.5px] ${status[1]}`}><span className={`size-1.5 rounded-full ${status[2]}`} />{status[0]}</span></div>
    <div className="border border-line">{rows.map(([label, value]) => <div key={label} className="grid grid-cols-[120px_minmax(0,1fr)] border-b border-line text-[10.5px] last:border-b-0"><div className="bg-inset px-3 py-2 text-ink-3">{label}</div><div className="px-3 py-2 font-mono text-ink-2">{value}</div></div>)}</div>
    <div className="mt-4 flex gap-2"><button className="h-7 rounded-[5px] bg-accent-tint px-2.5 text-[10.5px] font-medium text-accent-ink shadow-hairline">Sync now</button><button className="h-7 rounded-[5px] bg-field px-2.5 text-[10.5px] text-ink-2 shadow-hairline">Copy server config</button></div>
    <div className="mt-6"><div className="mb-1.5 text-[9px] font-semibold uppercase tracking-[0.1em] text-ink-3">Recent transfers</div>{[['13:09:12', 'push', 'bd91c42', '2 objects', 'complete'], ['13:05:11', 'push', '7ac3ef1', '9 objects', 'complete'], ['12:48:03', 'pull', 'refs/sessions/*', '4 objects', 'complete']].map((row) => <div key={row[0]} className="grid h-7 grid-cols-[70px_50px_1fr_80px_60px] items-center border-b border-line font-mono text-[9.5px] text-ink-3"><span>{row[0]}</span><span>{row[1]}</span><span className="text-accent-ink">{row[2]}</span><span>{row[3]}</span><span className="text-green">{row[4]}</span></div>)}</div>
  </section>
}
