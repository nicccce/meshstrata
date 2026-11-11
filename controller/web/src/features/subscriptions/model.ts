import type { DeepReadonly } from 'vue'
import type { ClientOptions, Draft, DraftData, GroupingOptions, Listener, Subscription, SubscriptionEntry, UsageSnapshot, Workspace } from '../../domain'
import { exitGroupName } from '../../domain/groups'

export type ReadWorkspace = DeepReadonly<Workspace>
export type SubscriptionForm = NonNullable<DraftData['subscription']>
export interface Finding { key: string; detail?: string; field?: string }
export interface PlannedEntry { entry: SubscriptionEntry; listener: Listener; server: string; reused: boolean }
export interface Plan { groups: Subscription['groups']; entries: PlannedEntry[]; errors: Finding[]; warnings: Finding[]; rules: string[]; dns: Record<string, string[]>; providers: Record<string, string[]> }
export const steps = ['basics', 'routes', 'policy', 'review'] as const
export const defaultGrouping: GroupingOptions = { allRoutes: true, byExit: true }
export const defaultClient: ClientOptions = { mode: 'tun', ipv6: false, allowBusinessUdp: false }
export function clone<T>(value: DeepReadonly<T> | T): T { return JSON.parse(JSON.stringify(value)) as T }
export function id(prefix: string) { return `${prefix}-${crypto.randomUUID()}` }
export function formFor(sub?: DeepReadonly<Subscription>): SubscriptionForm {
  return { description: sub?.description ?? '', expiresAt: sub?.expiresAt ?? new Date(Date.now() + 30 * 86400000).toISOString(), grouping: clone<GroupingOptions>(sub?.grouping ?? defaultGrouping), client: clone<ClientOptions>(sub?.client ?? defaultClient), selections: sub?.groups.map(g => ({ group: g.name, routeId: g.selectedRouteId })) ?? [] }
}
export function effectiveState(sub: DeepReadonly<Subscription>, now = Date.now()): Subscription['state'] {
  if (sub.state === 'revoked') return 'revoked'
  const remaining = Date.parse(sub.expiresAt) - now
  return remaining <= 0 ? 'expired' : remaining < 7 * 86400000 ? 'expiring' : 'active'
}
export function isPublished(route: ReadWorkspace['routes'][number]) { return !!route.published && !route.withdrawnAt }
function overlapsListener(a: Pick<Listener, 'nodeId' | 'port' | 'transport' | 'address'>, b: Pick<Listener, 'nodeId' | 'port' | 'transport' | 'address'>) {
  return a.nodeId === b.nodeId && a.port === b.port && (a.address === b.address || ['0.0.0.0', '::'].includes(a.address) || ['0.0.0.0', '::'].includes(b.address)) && (a.transport === 'tcp_udp' || b.transport === 'tcp_udp' || a.transport === b.transport)
}
export function buildPlan(ws: ReadWorkspace, draft: Draft, subscriptionId: string, now = Date.now()): Plan {
  const plan: Plan = { groups: [], entries: [], errors: [], warnings: [], rules: [], dns: {}, providers: {} }
  const error = (key: string, detail = '', field = 'review') => plan.errors.push({ key, detail, field })
  const warn = (key: string, detail = '') => plan.warnings.push({ key, detail })
  const form = draft.data?.subscription ?? formFor()
  if (!draft.name.trim() || draft.name.trim().length > 100) error('nameInvalid', '', 'basics')
  if (!Number.isFinite(Date.parse(form.expiresAt)) || Date.parse(form.expiresAt) <= now) error('expiryInvalid', '', 'basics')
  if (!draft.routeIds.length) error('routesRequired', '', 'routes')
  if (new Set(draft.routeIds).size !== draft.routeIds.length) error('duplicateRoutes', '', 'routes')
  const names = new Set<string>(['DIRECT', 'REJECT', 'COMPATIBLE'])
  const groupRevisions = new Map<string, number | undefined>()
  if (form.grouping.allRoutes && draft.routeIds.length) plan.groups.push({ name: 'All-Proxies', routeIds: [...draft.routeIds], selectedRouteId: draft.routeIds[0]! })
  for (const routeId of draft.routeIds) {
    const route = ws.routes.find(r => r.id === routeId)
    if (!route || !isPublished(route) || !route.published) { error('routeUnavailable', route?.name ?? routeId, 'routes'); continue }
    const revision = route.published
    if (!revision.nodeIds.length) { error('routeUnavailable', route.name, 'routes'); continue }
    if (!route.name.trim() || /[\r\n]/.test(route.name) || names.has(route.name)) error('duplicateName', route.name, 'routes')
    names.add(route.name)
    const pathNodes = revision.nodeIds.map(nodeId => ws.nodes.find(n => n.id === nodeId))
    if (pathNodes.some(n => !n || n.removedAt)) error('nodeMissing', route.name, 'routes')
    if (pathNodes.some(n => n?.control !== 'online')) warn('offlineEntry', route.name)
    if (revision.policyId) {
      const policy = ws.policies.find(p => p.id === revision.policyId)
      if (!policy || policy.revision !== revision.policyRevision || policy.terminalNodeId !== revision.nodeIds.at(-1)) error('policyRevision', route.name, 'routes')
      else {
        if (policy.rules.length > 1) warn('multipleEgress', route.name)
        for (const rule of policy.rules) if (rule.targetId !== 'REJECT' && !ws.targets.some(t => t.id === rule.targetId && t.nodeId === policy.terminalNodeId)) error('targetMissing', `${route.name}: ${rule.targetId}`)
      }
    }
    if (form.grouping.byExit) {
      const name = exitGroupName(revision)
      if (groupRevisions.has(name) && groupRevisions.get(name) !== revision.policyRevision) error('mixedRevision', name, 'policy')
      groupRevisions.set(name, revision.policyRevision)
      const group = plan.groups.find(g => g.name === name)
      if (group) group.routeIds.push(routeId)
      else plan.groups.push({ name, routeIds: [routeId], selectedRouteId: routeId })
    }
    const source = ws.listeners.find(l => l.id === revision.entryListenerId)
    const node = ws.nodes.find(n => n.id === revision.nodeIds[0])
    if (!source || !node || source.nodeId !== node.id || !node.address || /^(localhost|127\.|0\.0\.0\.0|::1$)/i.test(node.address) || node.mode === 'reverse') { error('entryMissing', route.name, 'routes'); continue }
    const protocol = node.protocolDefaults.find(p => p.protocol === source.protocol)
    const existing = ws.entries.find(e => e.subscriptionId === subscriptionId && e.routeId === routeId && e.cleanup !== 'confirmed' && e.identityState !== 'revoked')
    if (!existing && (!protocol || !protocol.enabled)) { error('protocolDisabled', `${node.name}: ${source.protocol}`, 'routes'); continue }
    if (form.client.allowBusinessUdp) {
      // No per-segment DNS/UDP verification contract exists: do not infer from transport support.
      error('udpUnverified', route.name, 'policy')
    }
    if (form.client.ipv6) error('ipv6Unverified', route.name, 'policy')
    const priorListener = existing && ws.listeners.find(l => l.id === existing.listenerId)
    if (existing && priorListener) {
      if (ws.listeners.some(l => l.id !== priorListener.id && overlapsListener(l, priorListener) && l.cleanup !== 'confirmed') || priorListener.nodeId !== node.id || priorListener.protocol !== source.protocol || existing.port !== priorListener.port || ws.entries.some(e => e.id !== existing.id && e.listenerId === priorListener.id && e.cleanup !== 'confirmed')) error('entryIsolation', existing.id)
      plan.entries.push({ entry: clone<SubscriptionEntry>(existing), listener: clone<Listener>(priorListener), server: node.address, reused: true })
      continue
    }
    if (existing) { error('entryMissing', existing.id); continue }
    const ranges = protocol?.portPool ?? node.portPool ?? [{ start: ws.settings.defaultPortStart, end: ws.settings.defaultPortEnd }]
    const occupied = (port: number) => ws.listeners.some(l => overlapsListener(l, { ...source, port }) && l.cleanup !== 'confirmed') || plan.entries.some(p => overlapsListener(p.listener, { ...source, port }))
    let port = protocol?.preferredPort ?? source.port
    if (protocol?.autoPort !== false) {
      port = 0
      for (const range of ranges) {
        for (let candidate = Math.max(1, range.start); candidate <= Math.min(65535, range.end); candidate++) {
          if (!occupied(candidate)) { port = candidate; break }
        }
        if (port) break
      }
      if (!port) { error('poolExhausted', node.name); continue }
    } else if (!Number.isInteger(port) || port < 1 || port > 65535 || occupied(port)) { error('portConflict', `${node.name}:${port}`); continue }
    const entryId = `${subscriptionId}-${routeId}`
    const listenerId = `listener-${entryId}`
    const timestamp = new Date(now).toISOString()
    plan.entries.push({ entry: { id: entryId, name: route.name, updatedAt: timestamp, subscriptionId, routeId, listenerId, port, identityState: 'pending', expiresAt: form.expiresAt, cleanup: 'none' }, listener: { ...clone<Listener>(source), id: listenerId, name: route.name, port, routeIds: [routeId], entryIds: [entryId], revision: 1, cleanup: 'none' }, server: node.address, reused: false })
  }
  for (const group of plan.groups) {
    if (names.has(group.name)) error('duplicateName', group.name, 'policy')
    names.add(group.name)
    if (!group.routeIds.length) error('emptyGroup', group.name, 'policy')
    const selected = form.selections.find(s => s.group === group.name)
    if (selected) {
      if (!group.routeIds.includes(selected.routeId)) error('initialMember', group.name, 'policy')
      else group.selectedRouteId = selected.routeId
    }
  }
  if (form.client.mode === 'system_proxy') warn('limitedCoverage')
  warn('coverageNotice')
  const profile = ws.profiles.find(p => p.id === draft.profileId)
  if (!profile) { error('profileRequired', '', 'policy'); return plan }
  const targets = new Set([...names].filter(n => n !== 'COMPATIBLE'))
  const domainClaims: { domain: string; target: string; suffix: boolean }[] = []
  const addDns = (domain: string, target: string, suffix: boolean) => {
    if (!/^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z0-9-]+$/.test(domain)) { error('domainInvalid', domain, 'policy'); return }
    domain = domain.toLowerCase()
    if (domainClaims.some(c => c.target !== target && (c.domain === domain || (c.suffix && domain.endsWith(`.${c.domain}`)) || (suffix && c.domain.endsWith(`.${domain}`))))) error('dnsConflict', domain, 'policy')
    domainClaims.push({ domain, target, suffix })
    // REJECT has no upstream; blocked domains never generate an invalid #REJECT resolver.
    if (target !== 'REJECT') plan.dns[`${suffix ? '+.' : ''}${domain}`] = [`https://1.1.1.1/dns-query#${target}`]
  }
  for (const setId of profile.domainSetIds) {
    const set = ws.domainSets.find(s => s.id === setId)
    if (!set || !set.domains.length || !/^[A-Za-z][\w-]*$/.test(set.identifier)) { error('domainSetMissing', setId, 'policy'); continue }
    if (plan.providers[set.identifier]) error('duplicateName', set.identifier, 'policy')
    plan.providers[set.identifier] = [...set.domains]
    if (!targets.has(set.group)) error('ruleTarget', `${set.identifier} → ${set.group}`, 'policy')
    set.domains.forEach(domain => addDns(domain, set.group, true))
  }
  const supported = new Set(['DOMAIN', 'DOMAIN-SUFFIX', 'DOMAIN-KEYWORD', 'DOMAIN-REGEX', 'GEOSITE', 'GEOIP', 'IP-CIDR', 'IP-CIDR6', 'IP-SUFFIX', 'IP-ASN', 'SRC-IP-CIDR', 'SRC-PORT', 'DST-PORT', 'IN-PORT', 'NETWORK', 'PROCESS-NAME', 'PROCESS-PATH', 'RULE-SET', 'MATCH'])
  const matches = profile.rules.filter(r => r.type === 'MATCH')
  if (matches.length !== 1) error('matchCount', String(matches.length), 'policy')
  if (matches[0]?.target === 'REJECT' && !plan.groups.some(g => g.name === 'All-Proxies')) error('dnsFallbackRequired', '', 'policy')
  if (!form.client.allowBusinessUdp) plan.rules.push('NETWORK,udp,REJECT')
  profile.rules.forEach((rule, index) => {
    if (!form.client.allowBusinessUdp && rule.id === 'business-udp-guard') return
    const context = `${index + 1}: ${rule.type},${rule.value},${rule.target}`
    if (!supported.has(rule.type) || /[\r\n,]/.test(rule.target) || !rule.target || (rule.type !== 'MATCH' && (!rule.value.trim() || /[\r\n,]/.test(rule.value))) || rule.options?.some(o => o !== 'no-resolve') || (rule.type === 'MATCH' && (!!rule.value || !!rule.options?.length))) error('ruleSyntax', context, 'policy')
    if (['NETWORK'].includes(rule.type) && !['tcp', 'udp'].includes(rule.value)) error('ruleSyntax', context, 'policy')
    if (['SRC-PORT', 'DST-PORT', 'IN-PORT'].includes(rule.type) && !rule.value.split('/').every(part => { const bounds = part.split('-').map(Number); return bounds.length <= 2 && bounds.every(n => Number.isInteger(n) && n >= 1 && n <= 65535) && (bounds.length === 1 || bounds[0]! <= bounds[1]!) })) error('ruleSyntax', context, 'policy')
    if (rule.type === 'IP-CIDR' || rule.type === 'SRC-IP-CIDR') {
      const [address, prefix] = rule.value.split('/')
      if (!address || address.split('.').length !== 4 || !address.split('.').every(n => /^\d{1,3}$/.test(n) && Number(n) <= 255) || !prefix || !/^\d+$/.test(prefix) || Number(prefix) > 32) error('ruleSyntax', context, 'policy')
    }
    if (!targets.has(rule.target)) error('ruleTarget', context, 'policy')
    if (rule.type === 'RULE-SET' && !plan.providers[rule.value]) error('domainSetMissing', context, 'policy')
    if (rule.type === 'RULE-SET') {
      const set = ws.domainSets.find(s => s.identifier === rule.value && profile.domainSetIds.includes(s.id))
      set?.domains.forEach(d => addDns(d, rule.target, true))
    } else if (rule.type === 'DOMAIN' || rule.type === 'DOMAIN-SUFFIX') addDns(rule.value, rule.target, rule.type === 'DOMAIN-SUFFIX')
    else if (rule.type !== 'MATCH') warn('dnsNotInferred', String(index + 1))
    if (rule.type !== 'MATCH') plan.rules.push([rule.type, rule.value, rule.target, ...(rule.options ?? [])].join(','))
  })
  for (const setId of profile.domainSetIds) {
    const set = ws.domainSets.find(s => s.id === setId)
    if (set && !profile.rules.some(r => r.type === 'RULE-SET' && r.value === set.identifier)) plan.rules.push(`RULE-SET,${set.identifier},${set.group}`)
  }
  if (matches.length === 1) plan.rules.push(`MATCH,${matches[0]!.target}`)
  return plan
}

export function draftFor(sub: DeepReadonly<Subscription>): Draft {
  return { id: `preview-${sub.id}`, feature: 'subscriptions', objectId: sub.id, name: sub.name, routeIds: [...sub.routeIds], nodeIds: [], profileId: sub.profileId, updatedAt: sub.updatedAt, data: { subscription: formFor(sub) } }
}
// Intentionally redacted examples: no session secret is ever serialized or downloaded.
export function renderConfig(ws: ReadWorkspace, draft: Draft, plan: Plan, providerOnly = false): string {
  const q = (s: string) => JSON.stringify(s)
  const lines = ['# SIMULATED EXAMPLE — placeholders are not usable proxy credentials.']
  const form = draft.data?.subscription ?? formFor()
  const fallback = plan.rules.find(r => r.startsWith('MATCH,'))?.slice(6) ?? 'DIRECT'
  const dnsFallback = fallback === 'REJECT' ? plan.groups.some(g => g.name === 'All-Proxies') ? 'All-Proxies' : 'REPLACE_WITH_DNS_OUTBOUND' : fallback
  if (!providerOnly) {
    lines.push(`mode: rule`, `ipv6: ${form.client.ipv6}`, 'tun:', `  enable: ${form.client.mode === 'tun'}`, '  stack: mixed', '  auto-route: true', '  dns-hijack: [any:53]', 'dns:', '  enable: true', '  enhanced-mode: fake-ip', `  ipv6: ${form.client.ipv6}`, '  fake-ip-range: 198.18.0.1/16', '  default-nameserver: [1.1.1.1]', '  proxy-server-nameserver: [https://1.1.1.1/dns-query#DIRECT]', `  nameserver: ${JSON.stringify([`https://1.1.1.1/dns-query#${dnsFallback}`])}`, '  nameserver-policy:')
    if (!Object.keys(plan.dns).length) lines[lines.length - 1] = '  nameserver-policy: {}'
    for (const [domain, servers] of Object.entries(plan.dns)) lines.push(`    ${q(domain)}: ${JSON.stringify(servers)}`)
  }
  lines.push('proxies:')
  for (const p of plan.entries) {
    const node = ws.nodes.find(n => n.id === p.listener.nodeId)
    const defaults = node?.protocolDefaults.find(d => d.protocol === p.listener.protocol)
    const routeName = ws.routes.find(r => r.id === p.entry.routeId)?.name ?? p.entry.routeId
    lines.push(`  - name: ${q(routeName)}`, `    type: ${p.listener.protocol === 'ss2022' ? 'ss' : p.listener.protocol}`, `    server: ${q(p.server)}`, `    port: ${p.entry.port}`)
    if (p.listener.protocol === 'ss2022') lines.push(`    cipher: ${q(defaults?.cipher ?? '2022-blake3-aes-128-gcm')}`, `    password: "REPLACE_WITH_ENTRY_KEY"`)
    if (p.listener.protocol === 'trojan' || p.listener.protocol === 'hysteria2') lines.push('    password: "REPLACE_WITH_ENTRY_PASSWORD"', `    sni: ${q(defaults?.serverName ?? p.server)}`, '    skip-cert-verify: false')
    if (p.listener.protocol === 'vless') lines.push('    uuid: "00000000-0000-4000-8000-000000000000"', '    tls: true', `    servername: ${q(defaults?.serverName ?? p.server)}`, '    skip-cert-verify: false')
    lines.push(`    udp: ${form.client.allowBusinessUdp}`)
  }
  if (providerOnly) return `${lines.join('\n')}\n`
  lines.push(plan.groups.length ? 'proxy-groups:' : 'proxy-groups: []')
  for (const group of plan.groups) {
    // Mihomo select initializes from its first member; persistent selection is separate from route order.
    const ordered = [group.selectedRouteId, ...group.routeIds.filter(r => r !== group.selectedRouteId)]
    lines.push(`  - name: ${q(group.name)}`, '    type: select', `    proxies: ${JSON.stringify(ordered.map(r => ws.routes.find(route => route.id === r)?.name ?? r))}`, '    empty-fallback: REJECT')
  }
  if (Object.keys(plan.providers).length) {
    lines.push('rule-providers:')
    for (const [name, domains] of Object.entries(plan.providers)) lines.push(`  ${q(name)}:`, '    type: inline', '    behavior: domain', `    payload: ${JSON.stringify(domains.map(d => `+.${d}`))}`)
  }
  lines.push('rules:', ...plan.rules.map(r => `  - ${q(r)}`))
  return `${lines.join('\n')}\n`
}
export interface UsageTotal { value: number | null; metric?: string; partial: boolean }
export function usageTotal(rows: readonly DeepReadonly<UsageSnapshot>[], entryIds: readonly string[]): UsageTotal {
  const selected = entryIds.map(entryId => rows.filter(r => r.entryId === entryId && r.scope === 'entry').sort((a, b) => b.queriedAt.localeCompare(a.queriedAt))[0])
  const first = selected[0]
  if (!first || !entryIds.length || first.metric === 'active_snapshot' || first.metric === 'unavailable' || !first.metric) return { value: null, partial: true }
  const comparable = selected.every(r => r && r.complete && !r.reset && r.metric === first.metric && r.coverageStart === first.coverageStart && r.coverageEnd === first.coverageEnd && (r.metric === 'cumulative_bytes' ? r.bytes !== null : r.connections != null))
  if (!comparable) return { value: null, metric: first.metric, partial: true }
  return { value: selected.reduce((sum, r) => sum + (r?.metric === 'cumulative_bytes' ? r.bytes ?? 0 : r?.connections ?? 0), 0), metric: first.metric, partial: false }
}
export function downloadExample(text: string, filename: string) {
  const blob = new Blob([text], { type: 'text/yaml;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a'); a.href = url; a.download = filename; a.click()
  setTimeout(() => URL.revokeObjectURL(url), 0)
}
