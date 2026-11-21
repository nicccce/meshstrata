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

// I03-C wire DTOs. These declarations do not activate live mode or issue requests.
// Every Pin is immutable; a new draft does not move a confirmed subscription.
export interface PinWire { id: ID; revision: Revision; digest: string }
export interface BusinessIssueWire { path: string; line: number; column: number; code: string }
export interface ProxyEndpointWire {
  host: string; port: number; protocol: 'ss2022' | 'trojan' | 'vless' | 'hysteria2'
  cipher: string; server_name: string; reality_public_key: string; short_id: string
  tls_leaf_sha256?: string
}
export interface HopWire { node_id: ID; node_config: PinWire; dns: PinWire; endpoint: ProxyEndpointWire; dial_ip: string }
export interface RouteWire {
  pin: PinWire; name: string; hops?: HopWire[]; egress: PinWire
  egress_is_default: boolean; business_udp: boolean; published: PinWire | null
}
export interface RouteVersionWire { draft: PinWire; published: PinWire | null; state: string; task_id: ID | null }
export interface CatalogWire { pin: PinWire; name: string; source?: string; sets?: PinWire[]; domains?: string[] }
export interface EgressTargetWire {
  node_id: ID; type: 'node_default' | 'interface' | 'http' | 'socks5'
  interface: string; host: string; dial_ip: string; port: number; authenticated: boolean
}
export interface EgressObservationWire {
  scope: 'tcp_canary'; business_udp: 'not_checked'; node_revision: Revision; dns: PinWire; target: PinWire
  interface: string; source_ip: string; observed_at: string; expires_at: string; current: boolean
}
export interface EgressWire {
  pin: PinWire; name: string; node_id: ID; target?: EgressTargetWire; dns?: PinWire
  targets?: PinWire[]; rules?: { destination: string; target: PinWire | null; no_resolve: boolean }[]
  verification: Verification; observation?: EgressObservationWire
}
export interface ClientPreferencesWire {
  mode: '' | 'tun' | 'system_proxy'; ipv6: boolean; business_udp: boolean
  all_routes_group: boolean; group_by_exit: boolean
  selections: { name: string; selected_entry_id: ID }[]
  dns: { bootstrap: string[]; proxy_servers: string[]; business: string[] }
}
export interface SubscriptionEntryWire {
  id: ID; identity_id: ID; generation: Revision; route: PinWire; name: string
  host?: string; port?: number; protocol?: ProxyEndpointWire['protocol']
  expires_at: string; revoked_at: string | null; sync: string; cleanup: string
}
export interface SubscriptionManagementWire {
  id: ID; name: string; revision: Revision; updated_at: string
  expires_at: string; revoked_at: string | null; profile: PinWire; entries: SubscriptionEntryWire[]; sync: string
}
export interface SubscriptionWire extends SubscriptionManagementWire {
  preferences: ClientPreferencesWire; desired_revision: Revision; confirmed_revision: Revision | null
  operations: { entry_id: ID; generation: Revision; state: string; cleanup: string; task_id: ID | null; cleanup_task_id: ID | null }[]
}
// Safe local-material references only. No path, PEM or private key can be returned.
export interface MaterialRefWire { id: ID; purpose: 'tls_certificate' | 'tls_private_key' | 'reality_private_key' }
export interface SocketSpecWire {
  tls_leaf_sha256?: string; address: string; requested_port: number; transport: 'tcp' | 'udp' | 'tcp_udp'
  certificate: MaterialRefWire | null; private_key: MaterialRefWire | null; reality_key: MaterialRefWire | null
  reality_destination: string; server_names: string[]; short_ids: string[]
}
export interface PinnedSocketWire { protocol: ProxyEndpointWire['protocol']; pin: PinWire; spec: SocketSpecWire; pool_first: number; pool_last: number }
export interface PreparationWire {
  node_id: ID; revision: Revision; prepared_revision: Revision
  dns: { pin: PinWire; bootstrap: string[]; resolvers: string[]; ipv6: boolean }; sockets: PinnedSocketWire[]
  dependencies: ID[]
  observation: { revision: Revision; observed_at: string; expires_at: string; current: boolean; capabilities: { available: boolean; protocol: ProxyEndpointWire['protocol']; business_udp: Verification }[] } | null
}
