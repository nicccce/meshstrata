// B00 wire types. Do not replace demo Workspace entities with server JSON casts.
export type Revision = number
export type ID = string
export type Verification = 'not_checked' | 'verified' | 'failed' | 'incomplete'
export interface Problem { code: string; fields?: { path: string; code: string; params?: Record<string, string> }[]; current_revision?: Revision }
export interface Page<T> { items: T[]; next_cursor: string | null }
export interface ListQuery { cursor?: string; limit?: number }
export interface Mutation { expected_revision: Revision; idempotency_key: string }
// B08 explicit POST /api/v1/subscriptions/{subscription_id}/download-link only.
// Sensitive HTTPS capability; keep in memory for Copy URL, never list/detail,
// drafts, frontend routes, errors, exports or analytics. Header setup is optional.
export interface SubscriptionDownloadLink { download_url: string }
export interface NodeWire {
  id: ID; name: string; revision: Revision; updated_at: string
  connection_mode: 'direct' | 'reverse_tunnel'; role: 'node' | 'both'
  control_address: string; proxy_address: string | null
  control: 'unknown' | 'online' | 'offline'; tunnel: 'unknown' | 'online' | 'offline'
  verification: Verification; target_dns_revision: Revision; last_confirmed_at: string | null
}
// A safe list/detail projection; feature-owned fields arrive with their own API contracts.
export interface NodeSummary {
  id: ID; name: string; revision: Revision; updatedAt: string
  mode: 'direct' | 'reverse'; role: 'node' | 'both'; controlAddress: string; address?: string
  control: NodeWire['control']; tunnel: NodeWire['tunnel']; verification: Verification
  targetDnsRevision: Revision; lastConfirmedAt?: string
}
export interface NodeResultWire {
  node_id: ID; target_revision: Revision; target_digest: string; applied: boolean
  verification: Verification; rollback: 'none' | 'pending' | 'confirmed' | 'failed'
  cleanup: 'none' | 'pending' | 'confirmed'; confirmed_at: string | null; issue?: Problem
}
export interface TaskWire {
  id: ID; revision: Revision; kind: 'publish' | 'rollback' | 'revoke' | 'cleanup' | 'query_status' | 'test_protocol' | 'measure' | 'check_egress' | 'query_usage' | 'read_logs'
  subject: { id: ID; revision: Revision }; state: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'timed_out'
  deadline: string; created_at: string; nodes: NodeResultWire[]
}
export interface DataAccess {
  readonly mode: 'demo' | 'live'
  listNodes(query: ListQuery, signal: AbortSignal): Promise<Page<NodeSummary>>
  getNode(id: ID, signal: AbortSignal): Promise<NodeSummary>
  renameNode(id: ID, name: string, mutation: Mutation, csrf: string, signal: AbortSignal): Promise<NodeSummary>
  getTask(id: ID, signal: AbortSignal): Promise<TaskWire>
}
