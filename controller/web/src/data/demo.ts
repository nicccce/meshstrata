import type { DataAccess, NodeSummary } from './contracts'

// Explicit read projection supplied by the caller. This adapter has no imports from
// live.ts and cannot trigger a network fallback. Existing demo commands remain in demo/workspace.
export function createDemoData(readNodes: () => readonly NodeSummary[]): DataAccess {
  const abort = (signal: AbortSignal) => signal.throwIfAborted()
  const unavailable = async (): Promise<never> => { throw new Error('NOT_IMPLEMENTED') }
  return {
    mode: 'demo',
    async listNodes(query, signal) {
      abort(signal)
      const limit = query.limit ?? 50
      if (!Number.isInteger(limit) || limit < 1 || limit > 200) throw new Error('INVALID_ARGUMENT')
      const items = readNodes().filter(n => !query.cursor || n.id > query.cursor).slice().sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0)
      const page = items.slice(0, limit).map(n => ({ ...n }))
      return { items: page, next_cursor: items.length > limit ? page.at(-1)!.id : null }
    },
    async getNode(id, signal) { abort(signal); const value = readNodes().find(n => n.id === id); if (!value) throw new Error('NOT_FOUND'); return { ...value } },
    renameNode: unavailable,
    getTask: unavailable,
  }
}
