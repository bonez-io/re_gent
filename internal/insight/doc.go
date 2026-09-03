// Package insight is the derived layer RFC 0007 builds over the capture
// tables: work items, entities, embeddings, and the queue and worker that
// produce them.
//
// The rule that shapes everything here is that nothing in this package runs
// inside an agent turn. A hook enqueues one row and, at most, starts a
// detached worker; the worker reads sessions, calls a configured provider,
// and writes derived rows in its own process. Every derived row is
// rebuildable from the capture tables and the object store, and every row is
// keyed by the provider and model that produced it, so a change of provider
// adds readings rather than invalidating them.
//
// This package owns the queue, the lock, the worker loop, the settings that
// resolve the two config files, and the enqueue hook. The read pipeline that
// turns turns into work items plugs in through Processor; until one is
// registered the worker leaves jobs queued and says so.
package insight
