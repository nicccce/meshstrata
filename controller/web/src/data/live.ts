import type { DataAccess, NodeSummary, NodeWire, Page, Problem, TaskWire } from './contracts'

export class APIError extends Error {
  constructor(readonly problem: Problem, readonly status: number) { super(problem.code) }
}
const record = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v)
const validID = (v: unknown): v is string => typeof v === 'string' && /^[a-z][a-z0-9_]{0,23}_[0-9a-f]{32}$/.test(v)
const revision = (v: unknown) => typeof v === 'number' && Number.isSafeInteger(v) && v > 0
const timestamp = (v: unknown) => typeof v === 'string' && /^\d{4}-\d\d-\d\dT/.test(v) && Number.isFinite(Date.parse(v))
const nullableTime = (v: unknown) => v === null || timestamp(v)
const member = (v: unknown, values: readonly unknown[]) => values.includes(v)
const verification = (v: unknown) => member(v, ['not_checked', 'verified', 'failed', 'incomplete'])
const invalid = () => new APIError({ code: 'INVALID_API_RESPONSE' }, 0)
function node(value: unknown): NodeSummary {
  if (!record(value) || !validID(value.id) || typeof value.name !== 'string' || !revision(value.revision) || !timestamp(value.updated_at)
    || !member(value.connection_mode, ['direct', 'reverse_tunnel']) || !member(value.role, ['node', 'both'])
    || typeof value.control_address !== 'string' || !(value.proxy_address === null || typeof value.proxy_address === 'string')
    || !member(value.control, ['unknown', 'online', 'offline']) || !member(value.tunnel, ['unknown', 'online', 'offline'])
    || !verification(value.verification) || !revision(value.target_dns_revision) || !nullableTime(value.last_confirmed_at)) throw invalid()
  const wire = value as unknown as NodeWire
  return { id: wire.id, name: wire.name, revision: wire.revision, updatedAt: wire.updated_at,
    mode: wire.connection_mode === 'reverse_tunnel' ? 'reverse' : 'direct', role: wire.role,
    controlAddress: wire.control_address, address: wire.proxy_address ?? undefined,
    control: wire.control, tunnel: wire.tunnel, verification: wire.verification,
    targetDnsRevision: wire.target_dns_revision, lastConfirmedAt: wire.last_confirmed_at ?? undefined }
}
function task(value: unknown): TaskWire {
  if (!record(value) || !validID(value.id) || !revision(value.revision) || !record(value.subject) || !validID(value.subject.id) || !revision(value.subject.revision)
    || !member(value.kind, ['publish','rollback','revoke','cleanup','query_status','test_protocol','measure','check_egress','query_usage','read_logs'])
    || !member(value.state, ['queued','running','succeeded','failed','cancelled','timed_out'])
    || !timestamp(value.deadline) || !timestamp(value.created_at) || !Array.isArray(value.nodes)) throw invalid()
  for (const n of value.nodes) {
    if (!record(n) || !validID(n.node_id) || !revision(n.target_revision) || typeof n.target_digest !== 'string' || !/^[0-9a-f]{64}$/.test(n.target_digest)
      || typeof n.applied !== 'boolean' || !verification(n.verification) || !member(n.rollback, ['none','pending','confirmed','failed'])
      || !member(n.cleanup, ['none','pending','confirmed']) || !nullableTime(n.confirmed_at) || (n.issue !== undefined && !problem(n.issue))
      || (n.verification === 'verified' && (!n.applied || n.confirmed_at === null || n.issue !== undefined))) throw invalid()
  }
  return value as unknown as TaskWire
}
function problem(value: unknown): value is Problem {
  return record(value) && typeof value.code === 'string' && /^[A-Z][A-Z0-9_]{0,63}$/.test(value.code)
    && (value.current_revision === undefined || revision(value.current_revision))
    && (value.fields === undefined || (Array.isArray(value.fields) && value.fields.every(f => record(f) && typeof f.path === 'string' && typeof f.code === 'string'
      && (f.params === undefined || (record(f.params) && Object.values(f.params).every(v => typeof v === 'string'))))))
}

// Construction is inert. No singleton, mode preference, auto request, retry or polling.
// Integration explicitly injects this adapter only once authentication + feature gates exist.
export function createLiveData(fetcher: typeof fetch = fetch): DataAccess {
  const request = async (path: string, signal: AbortSignal, body?: unknown, csrf?: string): Promise<unknown> => {
    const response = await fetcher(path, { signal, credentials: 'same-origin', cache: 'no-store', redirect: 'error',
      method: body === undefined ? 'GET' : 'PATCH',
      headers: body === undefined ? { Accept: 'application/json' } : { Accept: 'application/json', 'Content-Type': 'application/json', 'X-CSRF-Token': csrf ?? '' },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }) })
    if (!response.headers.get('content-type')?.includes('application/json')) throw invalid()
    const value: unknown = await response.json()
    if (!response.ok) throw new APIError(record(value) && problem(value.error) ? value.error : { code: 'INVALID_API_RESPONSE' }, response.status)
    return value
  }
  const identity = (id: string) => { if (!validID(id)) throw new APIError({ code: 'INVALID_ARGUMENT' }, 0); return encodeURIComponent(id) }
  return {
    mode: 'live',
    async listNodes(query, signal) {
      const limit = query.limit ?? 50
      if (!Number.isInteger(limit) || limit < 1 || limit > 200 || (query.cursor?.length ?? 0) > 512) throw new APIError({ code: 'INVALID_ARGUMENT' }, 0)
      const params = new URLSearchParams({ limit: String(limit) }); if (query.cursor) params.set('cursor', query.cursor)
      const value = await request(`/api/v1/nodes?${params}`, signal)
      if (!record(value) || !Array.isArray(value.items) || !(value.next_cursor === null || typeof value.next_cursor === 'string')) throw invalid()
      return { items: value.items.map(node), next_cursor: value.next_cursor } as Page<NodeSummary>
    },
    async getNode(id, signal) { return node(await request(`/api/v1/nodes/${identity(id)}`, signal)) },
    async renameNode(id, name, mutation, csrf, signal) {
      if (!revision(mutation.expected_revision) || !mutation.idempotency_key || mutation.idempotency_key.length > 128 || !name.trim() || name.length > 128 || !csrf) throw new APIError({ code: 'INVALID_ARGUMENT' }, 0)
      return node(await request(`/api/v1/nodes/${identity(id)}`, signal, { ...mutation, name }, csrf))
    },
    async getTask(id, signal) { return task(await request(`/api/v1/tasks/${identity(id)}`, signal)) },
  }
}
