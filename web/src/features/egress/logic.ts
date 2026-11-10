import type { EgressTarget, Listener } from '../../domain'
export interface Network { text: string; version: 4 | 6; start: bigint; end: bigint }
function ipv4(text: string): bigint | null {
 const parts = text.split('.')
 if (parts.length !== 4 || parts.some(p => !/^(0|[1-9]\d{0,2})$/.test(p) || Number(p) > 255)) return null
 return parts.reduce((n, p) => (n << 8n) + BigInt(p), 0n)
}
function ipv6(text: string): bigint | null {
 if (!/^[\da-f:]+$/i.test(text) || text.split('::').length > 2) return null
 const halves = text.split('::'); const left = halves[0] ? halves[0].split(':') : []; const right = halves[1] ? halves[1].split(':') : []
 if ([...left, ...right].some(p => !/^[\da-f]{1,4}$/i.test(p))) return null
 const missing = 8 - left.length - right.length
 if (halves.length === 1 ? missing !== 0 : missing < 1) return null
 return [...left, ...Array(halves.length === 2 ? missing : 0).fill('0'), ...right].reduce((n, p: string) => (n << 16n) + BigInt(`0x${p}`), 0n)
}
export function parseNetwork(input: string): Network | null {
 const [host, prefix, ...extra] = input.trim().split('/')
 if (!host || extra.length || (prefix !== undefined && !/^\d{1,3}$/.test(prefix))) return null
 const version = host.includes(':') ? 6 : 4; const bits = version === 4 ? 32 : 128
 const ip = version === 4 ? ipv4(host) : ipv6(host); const length = prefix === undefined ? bits : Number(prefix)
 if (ip === null || length > bits) return null
 const size = 1n << BigInt(bits - length); const start = ip / size * size
 const address = version === 4 ? [24n,16n,8n,0n].map(s => String((start >> s) & 255n)).join('.') : Array.from({ length: 8 }, (_, i) => ((start >> BigInt((7-i)*16)) & 65535n).toString(16)).join(':')
 return { text: `${address}/${length}`, version, start, end: start + size - 1n }
}
export function overlaps(a: Network, b: Network) { return a.version === b.version && a.start <= b.end && b.start <= a.end }
export function targetIssue(target: EgressTarget, listeners: readonly Pick<Listener, 'nodeId' | 'port'>[], ownAddresses: readonly string[]): string | null {
 if (!target.name.trim()) return 'targetName'
 if (target.type === 'node_default') return null
 if (target.type === 'interface') {
  if (!target.interfaceName || !/^[\w.:-]{1,32}$/.test(target.interfaceName)) return 'interfaceInvalid'
  if (/^(lo\d*|mihomo|clash|meta)$/i.test(target.interfaceName)) return 'loop'
  return null
 }
 const host = target.host?.trim().toLowerCase() ?? ''
 if (!host || host.length > 253 || /[\s/@?#]/.test(host) || (host.includes(':') ? ipv6(host) === null : ipv4(host) === null && (!/^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/i.test(host) || host.includes('..')))) return 'hostInvalid'
 if (!Number.isInteger(target.port) || !target.port || target.port < 1 || target.port > 65535) return 'portInvalid'
 const hostV6 = ipv6(host); const mappedLoopback = hostV6 !== null && (hostV6 >> 32n) === 65535n && ((hostV6 >> 24n) & 255n) === 127n
 const ownHost = ownAddresses.some(address => address.toLowerCase() === host || (hostV6 !== null && ipv6(address) === hostV6))
 const loopback = mappedLoopback || host === 'localhost' || host === '::1' || host === '0:0:0:0:0:0:0:1' || host.startsWith('127.') || host === '0.0.0.0' || host === '::'
 if ((loopback || ownHost) && listeners.some(l => l.nodeId === target.nodeId && l.port === target.port)) return 'loop'
 return null
}
export function newId(prefix: string) { return `${prefix}-${crypto.randomUUID()}` }
