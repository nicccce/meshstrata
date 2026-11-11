import type { DraftData } from '../domain'

type Mask = true | { [key: string]: Mask } | readonly [Mask]
const link: Mask = { fromNodeId: true, toNodeId: true, listenerId: true, protocol: true, tunnelId: true, tunnelRevision: true }
const revision: Mask = { revision: true, nodeIds: [true], links: [link], entryListenerId: true, policyId: true, policyRevision: true, validated: true, environment: true, allowUdpTransport: true, requireUdpRelay: true, description: true }
const mask: Mask = {
  node: { mode: true, address: true, location: true, city: true, environment: true },
  route: { exitNodeId: true, environment: true, allowUdpTransport: true, requireUdpRelay: true, currentPathId: true, selectedPaths: [{ id: true, name: true, revision }] },
  subscription: { description: true, expiresAt: true, grouping: { allRoutes: true, byExit: true }, client: { mode: true, ipv6: true, allowBusinessUdp: true, platform: true }, selections: [{ group: true, routeId: true }] },
  profile: { description: true, text: true, fallback: true, domainSetIds: [true] },
  egress: { custom: true, name: true, noResolve: true, ipv6: true, targets: [{ id: true, name: true, updatedAt: true, nodeId: true, type: true, interfaceName: true, host: true, port: true, tcp: true, udp: true }], rules: [{ id: true, destination: true, targetId: true }], fallback: true, selectedPolicy: true },
}
function project(value: unknown, fields: Mask): unknown {
  if (fields === true) return value === null || ['string', 'number', 'boolean'].includes(typeof value) ? value : undefined
  if (Array.isArray(fields)) return Array.isArray(value) ? value.map(v => project(v, fields[0]!)).filter(v => v !== undefined) : undefined
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  const result: Record<string, unknown> = {}
  for (const [key, child] of Object.entries(fields)) {
    const clean = project((value as Record<string, unknown>)[key], child)
    if (clean !== undefined) result[key] = clean
  }
  return result
}
export function projectDraftData(value: unknown): DraftData | undefined {
  return project(value, mask) as DraftData | undefined
}
