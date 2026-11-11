import type { DeepReadonly } from 'vue'
import type { AccessEnvironment, DraftData, Listener, Measurement, Node, Protocol, ProtocolSettings, RouteRevision, Workspace } from '../../domain'

export type RouteForm = NonNullable<DraftData['route']>
export type PathDraft = RouteForm['selectedPaths'][number]
export type ReadWorkspace = DeepReadonly<Workspace>
export type MeasurementMode = 'network' | 'browser' | 'partial'
export interface Candidate { id: string; nodeIds: string[] }
export interface Cost { mode: MeasurementMode; segments: (DeepReadonly<Measurement> | undefined)[]; total: number | null; nodeCost: number | null }
export const copy = <T>(value: DeepReadonly<T> | T): T => JSON.parse(JSON.stringify(value))
export const now = () => new Date().toISOString()
export const uid = (prefix: string) => `${prefix}-${crypto.randomUUID()}`
export function protocols(node: DeepReadonly<Node> | undefined, allowUdp: boolean): Protocol[] {
  return node?.protocolDefaults.filter(p => p.enabled && (allowUdp || p.protocol !== 'hysteria2')).map(p => p.protocol) ?? []
}
export function preferredProtocol(node: DeepReadonly<Node> | undefined, environment: AccessEnvironment, allowUdp: boolean, entry: boolean): Protocol | undefined {
  const enabled = protocols(node, allowUdp)
  const order: Protocol[] = entry && environment === 'strong_regulation' ? ['vless', 'trojan', 'ss2022', 'hysteria2'] : ['ss2022', 'trojan', 'vless', 'hysteria2']
  return order.find(p => enabled.includes(p))
}
function observation(ws: ReadWorkspace, from: string | undefined, to: string, kind: Measurement['kind'], environment: string, transport: string, tunnel = false) {
  return [...ws.measurements].reverse().find(m => m.kind === kind && m.fromNodeId === from && m.toNodeId === to && m.environment === environment && (kind === 'browser_http' || m.transport === transport) && (!tunnel || m.scope === 'reverse-tunnel'))
}
export function pathCost(ws: ReadWorkspace, nodeIds: readonly string[], environment: string, desired: 'network' | 'browser' = 'browser', transport: string | readonly string[] = 'tcp'): Cost {
  const segments = nodeIds.map((to, i) => observation(ws, i ? nodeIds[i - 1] : undefined, to, i || desired === 'network' ? 'network_rtt' : 'browser_http', environment, typeof transport === 'string' ? transport : transport[i] ?? 'tcp', ws.nodes.find(n => n.id === to)?.mode === 'reverse'))
  const fresh = (m: DeepReadonly<Measurement> | undefined) => !!m && m.value !== null && m.value >= 0 && m.samples > 0 && Date.parse(m.expiresAt) > Date.now()
  const complete = segments.length > 0 && segments.every(fresh) && (desired !== 'browser' || segments[0]?.timingBasis === 'responseStart-requestStart')
  const later = segments.slice(1)
  return { mode: complete ? desired : 'partial', segments, total: complete ? segments.reduce((sum, m) => sum + m!.value!, 0) : null, nodeCost: later.length && later.every(fresh) ? later.reduce((sum, m) => sum + m!.value!, 0) : null }
}
// Shortest known network path for each entry. Missing edges never receive a zero weight.
export function candidates(ws: ReadWorkspace, form: RouteForm, count: number): Candidate[] {
  const exit = ws.nodes.find(n => n.id === form.exitNodeId && !n.removedAt && n.control === 'online')
  if (!exit) return []
  if (exit.mode === 'reverse') {
    const relay = ws.nodes.find(n => n.id === ws.settings.relayNodeId && n.role === 'both' && n.control === 'online' && !n.removedAt)
    return relay && ws.settings.relay?.enabled !== false && !form.requireUdpRelay ? [{ id: `candidate-${relay.id}-${exit.id}`, nodeIds: [relay.id, exit.id] }] : []
  }
  const nodes = ws.nodes.filter(n => !n.removedAt && n.mode === 'direct' && n.control === 'online' && protocols(n, form.allowUdpTransport).length && (!form.requireUdpRelay || n.capabilities.udp === 'verified'))
  if (!nodes.some(n => n.id === exit.id)) return []
  const paths = nodes.map(entry => {
    if (entry.id === exit.id) return [exit.id]
    const distances = new Map<string, number>([[entry.id, 0]])
    const paths = new Map<string, string[]>([[entry.id, [entry.id]]])
    const visited = new Set<string>()
    while (visited.size < nodes.length) {
      const current = nodes.filter(n => !visited.has(n.id) && distances.has(n.id)).sort((a, b) => distances.get(a.id)! - distances.get(b.id)!)[0]
      if (!current || current.id === exit.id) break
      visited.add(current.id)
      for (const next of nodes.filter(n => !visited.has(n.id))) {
        const m = observation(ws, current.id, next.id, 'network_rtt', form.environment, 'tcp')
        if (!m || m.value === null || m.value < 0 || m.samples < 1 || Date.parse(m.expiresAt) <= Date.now()) continue
        const cost = distances.get(current.id)! + m.value
        if (cost < (distances.get(next.id) ?? Infinity)) { distances.set(next.id, cost); paths.set(next.id, [...paths.get(current.id)!, next.id]) }
      }
    }
    return paths.get(exit.id) ?? [entry.id, exit.id]
  })
  const unique = [...new Map(paths.map(path => [path.join('>'), path])).values()]
  unique.sort((a, b) => {
    const ac = pathCost(ws, a, form.environment).total; const bc = pathCost(ws, b, form.environment).total
    if (ac !== null && bc !== null) return ac - bc || a.length - b.length
    if (ac !== null) return -1
    if (bc !== null) return 1
    return a.length - b.length || a.join().localeCompare(b.join())
  })
  return unique.slice(0, count).map(nodeIds => ({ id: `candidate-${nodeIds.join('-')}`, nodeIds }))
}
export function listenerIds(revision: DeepReadonly<RouteRevision>): string[] { return [revision.entryListenerId, ...revision.links.map(l => l.listenerId)] }
export function pathIssues(ws: ReadWorkspace, form: RouteForm, path: PathDraft, editedRouteId?: string): string[] {
  const issues: string[] = []; const revision = path.revision
  const nodes = revision.nodeIds.map(id => ws.nodes.find(n => n.id === id))
  if (!path.name.trim()) issues.push('nameRequired')
  if (path.name.trim().length > 80) issues.push('nameLength')
  if (form.selectedPaths.some(p => p.id !== path.id && p.name.trim().toLowerCase() === path.name.trim().toLowerCase()) || ws.routes.some(r => r.id !== editedRouteId && r.id !== path.id && r.name.trim().toLowerCase() === path.name.trim().toLowerCase())) issues.push('nameConflict')
  if (!nodes.length || nodes.some(n => !n || n.removedAt)) issues.push('missingNode')
  if (new Set(revision.nodeIds).size !== nodes.length || nodes.length > 6 || revision.nodeIds.at(-1) !== form.exitNodeId) issues.push('pathInvalid')
  if (nodes.some(n => n?.control !== 'online')) issues.push('nodeOffline')
  if (revision.links.length !== nodes.length - 1 || revision.links.some((l, i) => l.fromNodeId !== revision.nodeIds[i] || l.toNodeId !== revision.nodeIds[i + 1])) issues.push('pathInvalid')
  const reverse = nodes.at(-1)?.mode === 'reverse'
  if (reverse && (nodes.length !== 2 || nodes[0]?.id !== ws.settings.relayNodeId || nodes[0]?.role !== 'both' || ws.settings.relay?.enabled === false || form.requireUdpRelay || revision.links[0]?.protocol !== 'ss2022')) issues.push('reverseInvalid')
  if (nodes.slice(0, -1).some(n => n?.mode === 'reverse')) issues.push('reverseInvalid')
  const ids = listenerIds(revision)
  ids.forEach((id, i) => {
    const listener = ws.listeners.find(l => l.id === id); const node = nodes[i]
    if (!listener || listener.nodeId !== node?.id) { issues.push('listenerMissing'); return }
    if (!protocols(node, form.allowUdpTransport).includes(listener.protocol)) issues.push('protocolInvalid')
    if (i && revision.links[i - 1]?.protocol !== listener.protocol) issues.push('protocolInvalid')
    if (reverse && i === 1 && (listener.protocol !== 'ss2022' || listener.transport !== 'tcp' || listener.address !== '127.0.0.1')) issues.push('reverseInvalid')
    if (!Number.isInteger(listener.port) || listener.port < 1 || listener.port > 65535) issues.push('portInvalid')
    const tcp = listener.transport !== 'udp'; const udp = listener.transport !== 'tcp'
    if (ws.listeners.some(l => l.id !== id && l.nodeId === listener.nodeId && l.port === listener.port && (l.address === listener.address || ['0.0.0.0', '::'].includes(l.address) || ['0.0.0.0', '::'].includes(listener.address)) && ((tcp && l.transport !== 'udp') || (udp && l.transport !== 'tcp')))) issues.push('portConflict')
    const next = revision.links[i]?.listenerId
    if (listener.nextHopListenerId !== undefined && listener.nextHopListenerId !== next) issues.push('listenerContext')
    if (ws.routes.some(r => r.id !== editedRouteId && r.published && listenerIds(r.published).includes(id)) && (next !== listener.nextHopListenerId || revision.policyId)) issues.push('listenerContext')
  })
  if (reverse) {
    const link = revision.links[0]; const tunnel = ws.tunnels.find(t => t.id === link?.tunnelId)
    if (!tunnel || tunnel.revision !== link?.tunnelRevision || tunnel.targetListenerId !== ids[1] || tunnel.relayNodeId !== nodes[0]?.id || tunnel.remoteNodeId !== nodes[1]?.id || tunnel.transport !== 'tcp_tls' || !Number.isInteger(tunnel.mappingPort) || tunnel.mappingPort < 1 || tunnel.mappingPort > 65535) issues.push('tunnelInvalid')
    else if (ws.tunnels.some(t => t.id !== tunnel.id && t.relayNodeId === tunnel.relayNodeId && t.mappingPort === tunnel.mappingPort)) issues.push('portConflict')
    if (nodes.at(-1)?.tunnel === 'offline') issues.push('tunnelOffline')
  }
  if (form.requireUdpRelay && (reverse || nodes.some(n => n?.capabilities.udp !== 'verified') || ids.some(id => ws.listeners.find(l => l.id === id)?.transport === 'tcp'))) issues.push('udpUnknown')
  if (revision.policyId) {
    const policy = ws.policies.find(p => p.id === revision.policyId)
    if (!policy || policy.terminalNodeId !== form.exitNodeId || policy.revision !== revision.policyRevision) issues.push('policyInvalid')
    else {
      if (policy.rules.filter(r => r.destination === 'MATCH').length !== 1 || policy.rules.at(-1)?.destination !== 'MATCH') issues.push('policyInvalid')
      const targets = policy.rules.filter(r => r.targetId !== 'REJECT').map(r => ws.targets.find(t => t.id === r.targetId))
      if (targets.some(t => !t || t.nodeId !== form.exitNodeId)) issues.push('policyInvalid')
      if (form.requireUdpRelay && targets.some(t => t?.udp !== 'verified')) issues.push('udpUnknown')
    }
  }
  return [...new Set(issues)]
}
export function freePort(ws: ReadWorkspace, nodeId: string, requested: number, transport: Listener['transport'], address: string, reserved: readonly Listener[] = [], defaults?: DeepReadonly<ProtocolSettings>): number | undefined {
  const node = ws.nodes.find(n => n.id === nodeId)
  const ranges = defaults?.portPool?.length ? defaults.portPool : node?.portPool?.length ? node.portPool : [{ start: ws.settings.defaultPortStart, end: ws.settings.defaultPortEnd }]
  const occupied = (port: number) => [...ws.listeners, ...reserved].some(l => l.nodeId === nodeId && l.port === port && (l.address === address || ['0.0.0.0', '::'].includes(l.address) || ['0.0.0.0', '::'].includes(address)) && (l.transport === 'tcp_udp' || transport === 'tcp_udp' || l.transport === transport))
  if (defaults?.autoPort === false) return Number.isInteger(requested) && requested > 0 && requested <= 65535 && !occupied(requested) ? requested : undefined
  if (Number.isInteger(requested) && requested > 0 && requested <= 65535 && ranges.some(r => requested >= r.start && requested <= r.end) && !occupied(requested)) return requested
  for (const range of ranges) for (let port = Math.max(1, range.start); port <= Math.min(65535, range.end); port++) if (!occupied(port)) return port
  return undefined
}
