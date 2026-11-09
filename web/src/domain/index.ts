export type ID = string
export type ConnectionState = 'online' | 'offline' | 'unknown'
export type VerificationState = 'not_checked' | 'verified' | 'failed' | 'incomplete'
export type Protocol = 'ss2022' | 'trojan' | 'vless' | 'hysteria2'
export interface Entity { id: ID; name: string; updatedAt: string }
export interface Issue { code: string; params?: Record<string, string | number>; field?: string }
export interface Node extends Entity {
  mode: 'direct' | 'reverse'; role: 'both' | 'node'; address?: string; controlAddress: string
  location: string; control: ConnectionState; tunnel: ConnectionState; verification: VerificationState
  protocolDefaults: { protocol: Protocol; enabled: boolean; preferredPort: number }[]
  targetDns: { revision: number; upstreams: string[] }; capabilities: { tcp: VerificationState; udp: VerificationState }
}
export interface Listener extends Entity { nodeId: ID; protocol: Protocol; address: string; port: number; transport: 'tcp' | 'udp' | 'tcp_udp'; routeIds: ID[]; revision: number }
export interface Link { fromNodeId: ID; toNodeId: ID; listenerId: ID; protocol: Protocol; tunnelId?: ID; tunnelRevision?: number }
export type ReleaseState = 'draft' | 'validating' | 'applying' | 'verifying' | 'published' | 'failed' | 'rolling_back' | 'rolled_back' | 'rollback_pending' | 'rollback_failed'
export interface RouteRevision { revision: number; nodeIds: ID[]; links: Link[]; entryListenerId: ID; policyId?: ID; policyRevision?: number; validated: boolean }
export interface Route extends Entity { state: ReleaseState; draft: RouteRevision; published?: RouteRevision; verification: VerificationState; cleanup: 'none' | 'pending' | 'confirmed' }
export interface ReverseTunnel extends Entity { relayNodeId: ID; remoteNodeId: ID; targetListenerId: ID; relayEndpoint: string; mappingPort: number; transport: 'tcp_tls'; revision: number; routeIds: ID[]; relayApplied: boolean; remoteApplied: boolean }
export interface EgressTarget extends Entity { nodeId: ID; type: 'node_default' | 'interface' | 'http' | 'socks5'; interfaceName?: string; host?: string; port?: number; tcp: VerificationState; udp: VerificationState }
export interface EgressPolicy extends Entity { terminalNodeId: ID; revision: number; rules: { id: ID; destination: string; targetId: ID | 'REJECT' }[] }
export interface Measurement extends Entity { routeId: ID; kind: 'network_rtt' | 'browser_http' | 'path_cost'; method: string; transport: string; value: number | null; samples: number; environment: string; startedAt: string; observedAt: string; expiresAt: string; scope: string; revision: number }
export interface EgressCheck extends Entity { routeId: ID; kind: 'destination' | 'egress_ip' | 'dns_path' | 'client_coverage'; status: 'observed' | 'failed' | 'not_supported' | 'not_checked'; scope: 'route' | 'target_only'; targetId?: ID; testUrl: string; observedAt: string; expiresAt: string; revision: number; value: string | null }
export interface DomainSet extends Entity { identifier: string; domains: string[]; group: string; revision: number }
export interface RuleProfile extends Entity { revision: number; rules: { id: ID; type: 'DOMAIN' | 'DOMAIN-SUFFIX' | 'MATCH'; value: string; target: string }[]; domainSetIds: ID[] }
export interface Subscription extends Entity { routeIds: ID[]; profileId: ID; entryIds: ID[]; expiresAt: string; state: 'active' | 'expiring' | 'expired' | 'revoked'; sync: 'synced' | 'pending'; groups: { name: string; routeIds: ID[]; selectedRouteId: ID }[] }
export interface SubscriptionEntry extends Entity { subscriptionId: ID; routeId: ID; listenerId: ID; port: number; identityState: 'active' | 'revoked' | 'pending'; expiresAt: string }
export interface Release extends Entity { kind: 'publish' | 'rollback' | 'revoke' | 'cleanup'; routeId?: ID; subscriptionId?: ID; state: ReleaseState; revision: number; nodes: { nodeId: ID; applied: boolean; verified: boolean; rollback: 'none' | 'pending' | 'confirmed' | 'failed'; issue?: Issue }[]; cleanup: 'none' | 'pending' | 'confirmed' }
export interface Event extends Entity { kind: string; entityType: 'node' | 'route' | 'subscription' | 'release'; entityId: ID; status: string; releaseId?: ID }
export interface UsageSnapshot extends Entity { subscriptionId: ID; scope: 'entry' | 'node' | 'tunnel'; bytes: number | null; coverageStart: string; coverageEnd: string; queriedAt: string; reset: boolean; complete: boolean }
export interface Settings { defaultPortStart: number; defaultPortEnd: number; relayNodeId: ID; targetDns: string[]; logLevel: 'warn' | 'info' }
export interface Draft { id: ID; feature: string; objectId?: ID; name: string; nodeIds: ID[]; routeIds: ID[]; profileId?: ID; step?: string; updatedAt: string }
export type Scenario = 'normal' | 'empty' | 'error' | 'stale' | 'offline' | 'partial'
export interface Workspace { schemaVersion: 1; scenario: Scenario; nodes: Node[]; listeners: Listener[]; routes: Route[]; tunnels: ReverseTunnel[]; targets: EgressTarget[]; policies: EgressPolicy[]; measurements: Measurement[]; checks: EgressCheck[]; profiles: RuleProfile[]; domainSets: DomainSet[]; subscriptions: Subscription[]; entries: SubscriptionEntry[]; releases: Release[]; events: Event[]; usage: UsageSnapshot[]; settings: Settings; drafts: Draft[] }
export type EntityCollection = Exclude<keyof Workspace, 'schemaVersion' | 'scenario' | 'settings'>
export type CollectionEntity<K extends EntityCollection> = Workspace[K][number]

export function issueMessageKey(code: string) { return code.startsWith('features.') ? code : `errors.${code}` }
