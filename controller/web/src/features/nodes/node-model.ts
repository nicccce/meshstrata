import type { AccessEnvironment, DraftData, Node, ProtocolSettings } from '../../domain'
import { networkRegulationLevels } from '../../domain/access-environment'

export type NodeForm = NonNullable<DraftData['node']>
export type EnrollmentStep = 'basics' | 'install' | 'verify'
export const enrollmentSteps: EnrollmentStep[] = ['basics', 'install', 'verify']
export const environments = networkRegulationLevels
export function newNodeForm(environment: AccessEnvironment = 'weak_regulation'): NodeForm {
  return { mode: 'direct', address: '', location: '', city: '', environment }
}

// Parse only; this never resolves a name or contacts an endpoint.
export function validAccessAddress(value: string): boolean {
  const address = value.trim()
  if (!address || /[\s/@?#\\]/.test(address) || address.includes('://')) return false
  try {
    const authority = address.includes(':') && !address.startsWith('[') && address.split(':').length > 2 ? `[${address}]` : address
    const parsed = new URL(`https://${authority}`)
    const host = parsed.hostname
    if (['localhost', '0.0.0.0', '[::]', '[::1]'].includes(host) || /^127\./.test(host)) return false
    return parsed.pathname === '/' && (host.startsWith('[') || host.split('.').every(label => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i.test(label)))
  } catch { return false }
}

export function locationKey(location: string): string {
  return /^[a-zA-Z0-9_., -]{1,140}$/.test(location) ? location : `loc-${Array.from(location).map(c => c.codePointAt(0)!.toString(16)).join('-').slice(0, 140)}`
}

export function statusMatches(node: Pick<Node, 'control' | 'mode' | 'tunnel' | 'verification' | 'removedAt'>, status: string): boolean {
  if (!status) return true
  if (['online', 'offline', 'unknown'].includes(status)) return node.control === status
  if (status === 'tunnel_offline') return node.mode === 'reverse' && node.tunnel === 'offline'
  if (status === 'failed') return node.verification === 'failed'
  if (status === 'not_checked') return node.verification === 'not_checked'
  if (status === 'removed') return !!node.removedAt
  return false
}

export function automaticLocation(node: Pick<Node, 'id' | 'location' | 'city'>): { location: string; city: string } {
  const samples: Record<string, { location: string; city: string }> = {
    hk: { location: 'HK', city: 'Hong Kong' }, sg: { location: 'SG', city: 'Singapore' },
    jp: { location: 'JP', city: 'Tokyo' }, relay: { location: 'Local', city: 'Demo city' },
  }
  const sample = samples[node.id] ?? { location: 'Demo region', city: 'Demo city' }
  // Older snapshots may not record whether an existing location was manual.
  return { location: node.location && node.location !== 'Unknown' ? node.location : sample.location, city: node.city || sample.city }
}

export function protocolDefaults(mode: NodeForm['mode'], port: number): ProtocolSettings[] {
  if (mode === 'reverse') return [{ protocol: 'ss2022', enabled: true, preferredPort: port, autoPort: true, listenAddress: '127.0.0.1', transport: 'tcp' }]
  return [
    { protocol: 'ss2022', enabled: true, preferredPort: port, autoPort: true, listenAddress: '0.0.0.0', transport: 'tcp_udp' },
    { protocol: 'trojan', enabled: true, preferredPort: port, autoPort: true, listenAddress: '0.0.0.0', transport: 'tcp' },
    { protocol: 'vless', enabled: true, preferredPort: port, autoPort: true, listenAddress: '0.0.0.0', transport: 'tcp', flow: 'xtls-rprx-vision' },
    { protocol: 'hysteria2', enabled: true, preferredPort: port, autoPort: true, listenAddress: '0.0.0.0', transport: 'udp' },
  ]
}
