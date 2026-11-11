import type { Workspace, Scenario, Route, Node } from '../domain'
export const seedTime = '2026-11-10T00:00:00.000Z'
const base = (id: string, name: string) => ({ id, name, updatedAt: seedTime })
export function createSeed(scenario: Scenario = 'normal'): Workspace {
  const nodes: Node[] = [['relay', 'Local Relay', 'both'], ['hk', 'HK-01', 'node'], ['sg', 'SG-01', 'node'], ['jp', 'JP-01', 'node'], ['reverse', 'Reverse Exit', 'node'], ['offline', 'Offline Node', 'node']].map(([id, name, role], i) => ({ ...base(id!, name!), mode: id === 'reverse' ? 'reverse' : 'direct', role: role as Node['role'], address: id === 'reverse' ? undefined : `${id}.example.invalid`, controlAddress: `control-${id}.example.invalid`, location: ['Local', 'HK', 'SG', 'JP', 'Remote', 'Unknown'][i]!, control: id === 'offline' ? 'offline' : 'online', tunnel: id === 'reverse' ? 'online' : 'unknown', verification: 'not_checked', protocolDefaults: [{ protocol: 'ss2022', enabled: true, preferredPort: 22001 }, ...(id === 'reverse' ? [] : [{ protocol: 'trojan' as const, enabled: true, preferredPort: 22443 }, { protocol: 'vless' as const, enabled: true, preferredPort: 22444 }, { protocol: 'hysteria2' as const, enabled: true, preferredPort: 22443 }])], targetDns: { revision: 1, upstreams: ['https://dns.example.invalid/dns-query'] }, capabilities: { tcp: 'not_checked', udp: id === 'reverse' ? 'failed' : 'not_checked' } }))
  const listeners = nodes.map((n, i) => ({ ...base(`listener-${n.id}`, `${n.name} SS`), nodeId: n.id, protocol: 'ss2022' as const, address: n.mode === 'reverse' ? '127.0.0.1' : '0.0.0.0', port: 22001 + i, transport: n.mode === 'reverse' ? 'tcp' as const : 'tcp_udp' as const, routeIds: [] as string[], revision: 1 }))
  const routes: Route[] = [['direct', ['hk']], ['multi', ['hk', 'sg', 'jp']], ['reverse-route', ['relay', 'reverse']], ['custom', ['sg']], ['draft-route', ['jp']], ['failed-route', ['hk', 'offline']]].map(([id, path]) => {
    const nodeIds = path as string[]; const routeId = id as string
    const revision = { revision: 1, nodeIds, entryListenerId: `listener-${nodeIds[0]}`, links: nodeIds.slice(1).map((to, i) => ({ fromNodeId: nodeIds[i]!, toNodeId: to, listenerId: `listener-${to}`, protocol: 'ss2022' as const, ...(to === 'reverse' ? { tunnelId: 'tunnel-reverse', tunnelRevision: 1 } : {}) })), ...(routeId === 'custom' ? { policyId: 'policy-sg', policyRevision: 1 } : {}), validated: false }
    return { ...base(routeId, `Demo ${routeId}`), state: routeId === 'draft-route' ? 'draft' : routeId === 'failed-route' ? 'failed' : 'published', draft: structuredClone(revision), ...(routeId === 'draft-route' ? {} : { published: structuredClone(revision) }), verification: routeId === 'failed-route' ? 'failed' : 'not_checked', cleanup: routeId === 'failed-route' ? 'pending' : 'none' }
  })
  listeners.forEach(l => { l.routeIds = routes.filter(r => r.draft.nodeIds.includes(l.nodeId)).map(r => r.id) })
  const subscriptions = ['active', 'expiring', 'expired', 'pending'].map((state, i) => ({ ...base(`sub-${state}`, `Demo ${state}`), routeIds: ['direct', 'custom'], profileId: i % 2 ? 'work' : 'default', entryIds: [`entry-${state}-direct`, `entry-${state}-custom`], expiresAt: ['2027-01-01T00:00:00Z', '2026-11-11T00:00:00Z', '2026-11-01T00:00:00Z', '2027-01-01T00:00:00Z'][i]!, state: state === 'pending' ? 'revoked' as const : state as 'active' | 'expiring' | 'expired', sync: state === 'pending' ? 'pending' as const : 'synced' as const, groups: [{ name: 'All-Proxies', routeIds: ['direct', 'custom'], selectedRouteId: 'direct' }] }))
  const ws: Workspace = { schemaVersion: 1, scenario, nodes, listeners, routes,
    tunnels: [{ ...base('tunnel-reverse', 'Demo TCP TLS tunnel'), relayNodeId: 'relay', remoteNodeId: 'reverse', targetListenerId: 'listener-reverse', relayEndpoint: 'relay.example.invalid:9444', mappingPort: 23001, transport: 'tcp_tls', revision: 1, routeIds: ['reverse-route'], relayApplied: true, remoteApplied: true }],
    targets: [{ ...base('target-socks', 'Local-SOCKS'), nodeId: 'sg', type: 'socks5', host: '127.0.0.1', port: 7890, tcp: 'not_checked', udp: 'not_checked' }],
    policies: [{ ...base('policy-sg', 'Demo custom egress'), terminalNodeId: 'sg', revision: 1, rules: [{ id: 'ip-rule', destination: '203.0.113.0/24', targetId: 'target-socks' }, { id: 'fallback', destination: 'MATCH', targetId: 'REJECT' }] }],
    measurements: [{ ...base('measurement-direct', 'Demo RTT'), routeId: 'direct', kind: 'network_rtt', method: 'simulated_tcp', transport: 'tcp', value: null, samples: 0, environment: 'weak_regulation', startedAt: seedTime, observedAt: seedTime, expiresAt: '2026-11-10T01:00:00Z', scope: 'entry-to-exit', revision: 1 }],
    checks: [{ ...base('check-custom', 'Demo egress check'), routeId: 'custom', kind: 'egress_ip', status: 'not_checked', scope: 'route', testUrl: 'https://echo.example.invalid', observedAt: seedTime, expiresAt: seedTime, revision: 1, value: null }],
    domainSets: [{ ...base('domains-work', 'Work domains'), identifier: 'WorkDomains', domains: ['work.example.invalid'], group: 'All-Proxies', revision: 1 }],
    profiles: ['default', 'work'].map(id => ({ ...base(id, id === 'default' ? 'Default' : 'Work'), revision: 1, rules: [{ id: `${id}-match`, type: 'MATCH', value: '', target: 'All-Proxies' }], domainSetIds: id === 'work' ? ['domains-work'] : [] })), subscriptions,
    entries: subscriptions.flatMap(s => s.routeIds.map(r => { const l = listeners.find(l => l.id === routes.find(route => route.id === r)!.draft.entryListenerId)!; return { ...base(`entry-${s.id.slice(4)}-${r}`, `Demo ${r} entry`), subscriptionId: s.id, routeId: r, listenerId: l.id, port: l.port, identityState: s.sync === 'pending' ? 'pending' as const : s.state === 'expired' ? 'revoked' as const : 'active' as const, expiresAt: s.expiresAt } })),
    releases: [{ ...base('release-failed', 'Simulated partial publish'), kind: 'publish', routeId: 'failed-route', state: 'rollback_pending', revision: 1, nodes: [{ nodeId: 'hk', applied: true, verified: false, rollback: 'confirmed' }, { nodeId: 'offline', applied: false, verified: false, rollback: 'pending', issue: { code: 'NODE_OFFLINE' } }], cleanup: 'pending' }],
    events: [{ ...base('event-failed', 'Simulated publish failure'), kind: 'publish', entityType: 'route', entityId: 'failed-route', status: 'failed', releaseId: 'release-failed' }],
    usage: [{ ...base('usage-active', 'Demo entry usage'), subscriptionId: 'sub-active', scope: 'entry', bytes: null, coverageStart: seedTime, coverageEnd: seedTime, queriedAt: seedTime, reset: false, complete: false }],
    settings: { environment: 'weak_regulation', defaultPortStart: 20000, defaultPortEnd: 29999, relayNodeId: 'relay', targetDns: ['https://dns.example.invalid/dns-query'], logLevel: 'warn' }, drafts: [] }
  // Subscription SS 2022 identities require isolated listeners and actual ports.
  ws.entries.forEach((entry, i) => {
    const source = ws.listeners.find(listener => listener.id === entry.listenerId)!
    const isolated = { ...source, id: `listener-${entry.id}`, name: `${entry.name} isolated`, port: 24000 + i, routeIds: [entry.routeId], entryIds: [entry.id] }
    ws.listeners.push(isolated)
    entry.listenerId = isolated.id
    entry.port = isolated.port
  })
  if (scenario === 'empty') { for (const key of Object.keys(ws) as (keyof Workspace)[]) if (Array.isArray(ws[key])) (ws[key] as unknown[]) .length = 0 }
  if (scenario === 'offline') nodes.forEach(n => { n.control = 'offline'; n.tunnel = 'offline' })
  if (scenario === 'stale') ws.measurements.forEach(m => { m.value = 42; m.samples = 3; m.expiresAt = '2020-01-01T00:00:00Z' })
  if (scenario === 'partial') { ws.usage[0]!.bytes = 2048; ws.releases[0]!.nodes[0]!.verified = true }
  return ws
}
