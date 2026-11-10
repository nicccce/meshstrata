import type { DeepReadonly } from 'vue'
import type { Node, Protocol, ProtocolSettings, Listener, Route, Workspace, Issue } from '../../domain'

export type ReadonlyNode = DeepReadonly<Node>
export const protocols: Protocol[] = ['ss2022', 'trojan', 'vless', 'hysteria2']
export const cloneNode = (node: ReadonlyNode): Node => JSON.parse(JSON.stringify(node))
export const cloneListener = (listener: DeepReadonly<Listener>): Listener => JSON.parse(JSON.stringify(listener))
export const cloneRoute = (route: DeepReadonly<Route>): Route => JSON.parse(JSON.stringify(route))
export const issue = (code: string, field?: string, params?: Issue['params']): Issue => ({ code: `features.node-detail.errors.${code}`, field, params })
export const transportFor = (protocol: Protocol): Listener['transport'] => protocol === 'hysteria2' ? 'udp' : protocol === 'ss2022' ? 'tcp_udp' : 'tcp'
export function defaultsFor(node: ReadonlyNode): ProtocolSettings[] {
  return (node.mode === 'reverse' ? ['ss2022'] as const : protocols).map(protocol => {
    const current = node.protocolDefaults.find(item => item.protocol === protocol)
    const settings: ProtocolSettings = {
      protocol, enabled: current?.enabled ?? false, preferredPort: current?.preferredPort ?? (protocol === 'ss2022' ? 22001 : protocol === 'vless' ? 22444 : 22443),
      autoPort: current?.autoPort ?? true, listenAddress: current?.listenAddress ?? (node.mode === 'reverse' ? '127.0.0.1' : '0.0.0.0'),
      transport: current?.transport ?? transportFor(protocol), portPool: current?.portPool?.map(range => ({ ...range })) ?? [],
      cipher: current?.cipher ?? '2022-blake3-aes-128-gcm', serverName: current?.serverName ?? '',
      publicKey: current?.publicKey ?? '', shortId: current?.shortId ?? '', flow: current?.flow ?? 'xtls-rprx-vision',
    }
    if (node.mode === 'reverse') { settings.listenAddress = '127.0.0.1'; settings.transport = 'tcp' }
    return settings
  })
}
export function relatedRoutes(ws: DeepReadonly<Workspace>, nodeId: string) {
  const listenerIds = new Set(ws.listeners.filter(l => l.nodeId === nodeId).map(l => l.id))
  const tunnelIds = new Set(ws.tunnels.filter(t => t.remoteNodeId === nodeId || t.relayNodeId === nodeId).map(t => t.id))
  return ws.routes.filter(route => [route.draft, route.published].some(rev => rev && (rev.nodeIds.includes(nodeId) || listenerIds.has(rev.entryListenerId) || rev.links.some(link => link.fromNodeId === nodeId || link.toNodeId === nodeId || listenerIds.has(link.listenerId) || (link.tunnelId && tunnelIds.has(link.tunnelId))))))
}
export function routeRoles(route: DeepReadonly<Route>, nodeId: string): string[] {
  const roles = new Set<string>()
  for (const rev of [route.published, route.draft]) {
    if (!rev) continue
    const i = rev.nodeIds.indexOf(nodeId)
    if (i < 0) continue
    if (i === 0) roles.add('entry')
    if (i === rev.nodeIds.length - 1) roles.add('exit')
    if (i > 0 && i < rev.nodeIds.length - 1) roles.add('transit')
  }
  return [...roles]
}
export const validPort = (port: number | null) => port !== null && Number.isInteger(port) && port >= 1 && port <= 65535
export function validIp(input: string): boolean {
  if (/^\d+\.\d+\.\d+\.\d+$/.test(input)) return input.split('.').every(part => Number(part) <= 255 && String(Number(part)) === part)
  if (!input.includes(':') || /[%\s/\[\]]/.test(input)) return false
  try { return Boolean(new URL(`http://[${input}]/`).hostname) } catch { return false }
}
export function validHost(input: string) { return validIp(input) || (input.length <= 253 && input.split('.').every(label => /^[a-z\d](?:[a-z\d-]{0,61}[a-z\d])?$/i.test(label))) }
export function safeHttps(input: string): boolean {
  try { const url = new URL(input); return url.protocol === 'https:' && !url.username && !url.password && !url.hash && !url.search && Boolean(url.hostname) } catch { return false }
}
export const lines = (input: string) => input.split(/[\n,]+/).map(value => value.trim()).filter(Boolean)
export function validUpstream(input: string) {
  if (validIp(input)) return true
  try { const url = new URL(input); return ['https:', 'tls:'].includes(url.protocol) && !url.username && !url.password && !url.hash && !url.search && Boolean(url.hostname) } catch { return false }
}
export function overlaps(a: string, b: string) { return a === b || ['0.0.0.0', '::'].includes(a) || ['0.0.0.0', '::'].includes(b) }
export function portConflict(listeners: readonly DeepReadonly<Listener>[], nodeId: string, address: string, port: number, transport: Listener['transport'], exceptId?: string) {
  return listeners.find(l => l.id !== exceptId && l.nodeId === nodeId && l.port === port && l.cleanup !== 'confirmed' && overlaps(l.address, address) && (l.transport === 'tcp_udp' || transport === 'tcp_udp' || l.transport === transport))
}
export function validateDefaults(node: ReadonlyNode, settings: ProtocolSettings[], listeners: readonly DeepReadonly<Listener>[]): Issue[] {
  const baseline = defaultsFor(node)
  const issues: Issue[] = []
  for (const row of settings) {
    const field = `protocol-${row.protocol}`
    if (!validIp(row.listenAddress ?? '')) issues.push(issue('LISTEN_ADDRESS', field))
    if (!validPort(row.preferredPort)) issues.push(issue('PORT', field))
    if (row.portPool?.some(range => !validPort(range.start) || !validPort(range.end) || range.start > range.end)) issues.push(issue('POOL', field))
    if (row.protocol === 'ss2022' && !['2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm'].includes(row.cipher ?? '')) issues.push(issue('CIPHER', field))
    if (row.serverName && !validHost(row.serverName)) issues.push(issue('SERVER_NAME', field))
    if (row.shortId && !/^(?:[a-f\d]{2}){1,8}$/i.test(row.shortId)) issues.push(issue('SHORT_ID', field))
    if (row.publicKey && !/^[\w-]{43}$/.test(row.publicKey)) issues.push(issue('PUBLIC_KEY', field))
    const before = baseline.find(item => item.protocol === row.protocol)
    const changed = !before || before.preferredPort !== row.preferredPort || before.autoPort !== row.autoPort || before.listenAddress !== row.listenAddress || before.transport !== row.transport || (!before.enabled && row.enabled)
    if (row.enabled && !row.autoPort && changed) {
      const conflict = portConflict(listeners, node.id, row.listenAddress ?? '', row.preferredPort, row.transport ?? transportFor(row.protocol))
      if (conflict) issues.push(issue('PORT_IN_USE', field, { port: row.preferredPort, name: conflict.name }))
    }
    if (row.enabled && !row.autoPort) {
      const other = settings.find(item => item.protocol !== row.protocol && item.enabled && !item.autoPort && item.preferredPort === row.preferredPort && overlaps(item.listenAddress ?? '', row.listenAddress ?? '') && (item.transport === 'tcp_udp' || row.transport === 'tcp_udp' || item.transport === row.transport))
      if (other) issues.push(issue('DEFAULT_CONFLICT', field, { port: row.preferredPort }))
    }
  }
  return issues
}
