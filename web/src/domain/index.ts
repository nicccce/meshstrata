export type ID = string
export type ConnectionState = 'online' | 'offline' | 'unknown'
export type VerificationState = 'not_checked' | 'verified' | 'failed' | 'incomplete'
export type Protocol = 'ss2022' | 'trojan' | 'vless' | 'hysteria2'
export interface Entity { id: ID; name: string; updatedAt: string }
export interface Issue { code: string; params?: Record<string, string | number>; field?: string }
export interface Node extends Entity {
  mode: 'direct' | 'reverse'; role: 'both' | 'node'; address?: string; controlAddress: string
  location: string; control: ConnectionState; tunnel: ConnectionState; verification: VerificationState
  protocolDefaults: ProtocolSettings[]
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
export interface RuleLine { id: ID; type: string; value: string; target: string; options?: string[] }
export interface RuleProfile extends Entity { revision: number; rules: RuleLine[]; domainSetIds: ID[]; description?: string; sourceText?: string }
export interface Subscription extends Entity { routeIds: ID[]; profileId: ID; entryIds: ID[]; expiresAt: string; state: 'active' | 'expiring' | 'expired' | 'revoked'; sync: 'synced' | 'pending'; groups: { name: string; routeIds: ID[]; selectedRouteId: ID }[] }
export interface SubscriptionEntry extends Entity { subscriptionId: ID; routeId: ID; listenerId: ID; port: number; identityState: 'active' | 'revoked' | 'pending'; expiresAt: string }
export interface Release extends Entity { kind: 'publish' | 'rollback' | 'revoke' | 'cleanup'; routeId?: ID; subscriptionId?: ID; state: ReleaseState; revision: number; nodes: { nodeId: ID; applied: boolean; verified: boolean; rollback: 'none' | 'pending' | 'confirmed' | 'failed'; issue?: Issue }[]; cleanup: 'none' | 'pending' | 'confirmed' }
export interface Event extends Entity { kind: string; entityType: 'node' | 'route' | 'subscription' | 'release'; entityId: ID; status: string; releaseId?: ID }
export interface UsageSnapshot extends Entity { subscriptionId: ID; scope: 'entry' | 'node' | 'tunnel'; bytes: number | null; coverageStart: string; coverageEnd: string; queriedAt: string; reset: boolean; complete: boolean }
export interface Settings { defaultPortStart: number; defaultPortEnd: number; relayNodeId: ID; targetDns: string[]; logLevel: 'error' | 'warn' | 'info' | 'debug' }
export interface Draft { id: ID; feature: string; objectId?: ID; name: string; nodeIds: ID[]; routeIds: ID[]; profileId?: ID; step?: string; updatedAt: string; data?: DraftData }
export type Scenario = 'normal' | 'empty' | 'error' | 'stale' | 'offline' | 'partial'
export interface Workspace { schemaVersion: 1; scenario: Scenario; nodes: Node[]; listeners: Listener[]; routes: Route[]; tunnels: ReverseTunnel[]; targets: EgressTarget[]; policies: EgressPolicy[]; measurements: Measurement[]; checks: EgressCheck[]; profiles: RuleProfile[]; domainSets: DomainSet[]; subscriptions: Subscription[]; entries: SubscriptionEntry[]; releases: Release[]; events: Event[]; usage: UsageSnapshot[]; settings: Settings; drafts: Draft[] }
export type EntityCollection = Exclude<keyof Workspace, 'schemaVersion' | 'scenario' | 'settings'>
export type CollectionEntity<K extends EntityCollection> = Workspace[K][number]

export function issueMessageKey(code: string) { return code.startsWith('features.') ? code : `errors.${code}` }

// Optional fields preserve F00 snapshots while supplying the page contracts.
export type AccessEnvironment = 'mainland_china' | 'outside_mainland_china'
export interface PortRange { start: number; end: number }
export interface ProtocolSettings {
  protocol: Protocol; enabled: boolean; preferredPort: number; autoPort?: boolean
  listenAddress?: string; portPool?: PortRange[]; transport?: 'tcp' | 'udp' | 'tcp_udp'
  serverName?: string; cipher?: string; publicKey?: string; shortId?: string; flow?: string
}
export interface Node {
  city?: string; environment?: AccessEnvironment; tags?: string[]; manualLocation?: boolean
  agentVersion?: string; mihomoVersion?: string; lastConfirmedAt?: string; queriedAt?: string
  queryIssue?: Issue; appliedRevision?: number; removedAt?: string; observedSourceIp?: string
  resources?: { cpuPercent: number | null; memoryBytes: number | null; diskBytes: number | null; networkBytesPerSecond: number | null }
  portPool?: PortRange[]
  dnsDraft?: { upstreams: string[]; outbound: string; bootstrap: string[] }
  dnsRouting?: { outbound: string; bootstrap: string[] }
  probe?: { enabled: boolean; url: string; revision: number; status: VerificationState }
}
export interface Listener { nextHopListenerId?: ID; entryIds?: ID[]; state?: ReleaseState; cleanup?: 'none' | 'pending' | 'confirmed' }
export interface RouteRevision { environment?: AccessEnvironment; allowUdpTransport?: boolean; requireUdpRelay?: boolean; description?: string }
export interface Route { verifiedAt?: string; withdrawnAt?: string }
export interface EgressPolicy { noResolve?: boolean; ipv6?: boolean }
export interface Measurement { fromNodeId?: ID; toNodeId?: ID; timingBasis?: string; dispersion?: number; failedSamples?: number; pathVerified?: boolean }
export interface EgressCheck { subscriptionId?: ID; entryId?: ID; policyId?: ID; matchedRuleId?: ID; selectedTargetId?: ID; ruleResolutionIp?: string; issue?: Issue }
export interface GroupingOptions { allRoutes: boolean; byExit: boolean }
export interface ClientOptions { mode: 'tun' | 'system_proxy'; ipv6: boolean; allowBusinessUdp: boolean; platform?: string }
export interface Subscription { description?: string; grouping?: GroupingOptions; client?: ClientOptions; downloadVersion?: number }
export interface SubscriptionEntry { cleanup?: 'none' | 'pending' | 'confirmed' }
export interface ReleaseStage { id: ID; name: string; nodeId?: ID; state: 'pending' | 'running' | 'succeeded' | 'failed' | 'not_checked'; issue?: Issue; completedAt?: string }
export interface Release { nodeId?: ID; stages?: ReleaseStage[]; startedAt?: string; completedAt?: string; issue?: Issue; sourceDraftId?: ID }
export interface UsageSnapshot { entryId?: ID; metric?: 'cumulative_bytes' | 'connection_count' | 'active_snapshot' | 'unavailable'; uploadBytes?: number | null; downloadBytes?: number | null; connections?: number | null }
export interface Settings {
  environment?: AccessEnvironment; allowUdpTransport?: boolean; requireUdpRelay?: boolean
  measurementTtlHours?: number; failureTtlMinutes?: number; locale?: string
  webPort?: number; controlPort?: number
  relay?: { enabled: boolean; endpoint: string; mappingPool: PortRange }
  locationService?: { provider: string; configured: boolean; preserveManual: boolean }
  certificates?: { domain: string; provider: string; method: 'dns01' | 'import'; status: VerificationState }
  localClient?: { address: string; paired: boolean; allowedOrigin: string; rawRtt: boolean }
  logs?: { fileMiB: number; fileCount: number; totalMiB: number; retentionDays: number; debugMinutes: number; debugUntil?: string; previousLevel?: 'error' | 'warn' | 'info' }
}
// Explicitly non-sensitive fields only. Runtime projection is in demo/draft-data.ts.
export interface DraftData {
  node?: { mode: 'direct' | 'reverse'; address: string; location: string; city?: string; environment?: AccessEnvironment }
  route?: { exitNodeId: ID; environment: AccessEnvironment; allowUdpTransport: boolean; requireUdpRelay: boolean; selectedPaths: { id: ID; name: string; revision: RouteRevision }[]; currentPathId?: ID }
  subscription?: { description: string; expiresAt: string; grouping: GroupingOptions; client: ClientOptions; selections: { group: string; routeId: ID }[] }
  profile?: { description: string; text: string; fallback: string; domainSetIds: ID[] }
  egress?: { custom: boolean; name: string; noResolve: boolean; ipv6: boolean; targets: EgressTarget[]; rules: EgressPolicy['rules']; fallback: ID; selectedPolicy: ID | null }
}
