import type { DeepReadonly } from 'vue'
import type { DomainSet, RuleLine, RuleProfile, Workspace } from '../../domain'
import { exitGroupName } from '../../domain/groups'

export type RuleIssue = { code: string; line?: number; target?: string; detail?: string; warning?: boolean }
export type ParsedLine = RuleLine & { line: number }
export const supportedTypes = ['DOMAIN', 'DOMAIN-SUFFIX', 'RULE-SET', 'IP-CIDR', 'IP-CIDR6', 'PROCESS-NAME', 'PROCESS-PATH', 'DST-PORT', 'SRC-PORT', 'NETWORK', 'MATCH']
export function containsSensitiveInput(text: string) {
  return /(?:password|passwd|secret|token|credential|authorization|private[-_ ]?key)\s*[:=]|[a-z][a-z0-9+.-]*:\/\/[^\s/]*@/i.test(text)
}
export function isDomain(value: string) {
  return value.length <= 253 && value.includes('.') && !/^\d+(?:\.\d+){3}$/.test(value) && value.split('.').every(label => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i.test(label))
}
function isCidr(value: string, ipv6: boolean) {
  const [address, mask, extra] = value.split('/')
  if (extra !== undefined || mask === undefined || !/^\d+$/.test(mask)) return false
  if (!ipv6) return Number(mask) <= 32 && !!address && address.split('.').length === 4 && address.split('.').every(v => /^\d{1,3}$/.test(v) && Number(v) <= 255)
  if (Number(mask) > 128 || !address || !/^[0-9a-f:]+$/i.test(address) || address.includes(':::')) return false
  const halves = address.split('::')
  if (halves.length > 2) return false
  const parts = halves.flatMap(v => v ? v.split(':') : [])
  return parts.every(v => /^[0-9a-f]{1,4}$/i.test(v)) && (halves.length === 2 ? parts.length < 8 : parts.length === 8)
}
export function parseRules(text: string): { lines: ParsedLine[]; issues: RuleIssue[] } {
  const lines: ParsedLine[] = [], issues: RuleIssue[] = []
  let hasHeader = false
  text.split(/\r?\n/).forEach((original, index) => {
    const line = index + 1
    let raw = original.trim()
    if (!raw || raw.startsWith('#')) return
    if (raw === 'rules:') { if (hasHeader || lines.length) issues.push({ code: 'section', line }); hasHeader = true; return }
    raw = raw.replace(/^-\s+/, '')
    if ((raw.startsWith('"') && raw.endsWith('"')) || (raw.startsWith("'") && raw.endsWith("'"))) raw = raw.slice(1, -1)
    const fields = raw.split(',').map(v => v.trim())
    const type = fields[0] ?? ''
    if (!supportedTypes.includes(type)) { issues.push({ code: 'unsupported', line, detail: type }); return }
    const match = type === 'MATCH', expected = match ? 2 : 3
    const options = fields.slice(expected)
    if (fields.length < expected || fields.slice(0, expected).some(v => !v) || (options.length && (!['IP-CIDR', 'IP-CIDR6'].includes(type) || options.length !== 1 || options[0] !== 'no-resolve'))) {
      issues.push({ code: 'syntax', line }); return
    }
    const value = match ? '' : fields[1]!, target = fields[match ? 1 : 2]!
    if ((['DOMAIN', 'DOMAIN-SUFFIX'].includes(type) && !isDomain(value)) || (type === 'RULE-SET' && !/^[A-Za-z][A-Za-z0-9_-]*$/.test(value)) || (type === 'IP-CIDR' && !isCidr(value, false)) || (type === 'IP-CIDR6' && !isCidr(value, true)) || (['DST-PORT', 'SRC-PORT'].includes(type) && (!/^\d+$/.test(value) || Number(value) < 1 || Number(value) > 65535)) || (type === 'NETWORK' && !['tcp', 'udp'].includes(value))) issues.push({ code: 'value', line, detail: value })
    if (/[#\r\n]/.test(target)) issues.push({ code: 'targetSyntax', line, target })
    lines.push({ id: `line-${line}`, type, value, target, ...(options.length ? { options } : {}), line })
  })
  return { lines, issues }
}
export function normalizeMatchText(text: string, target: string) {
  const matches = new Set(parseRules(text).lines.filter(line => line.type === 'MATCH').map(line => line.line))
  const kept = text.split(/\r?\n/).filter((_line, index) => !matches.has(index + 1)).join('\n').trimEnd()
  const match = `${/^\s*rules:\s*$/m.test(kept) ? '  - ' : ''}MATCH,${target}`
  return `${kept}${kept ? '\n' : ''}${match}`
}
export function serializeRules(rules: readonly DeepReadonly<RuleLine>[]) {
  return rules.map(rule => [rule.type, ...(rule.type === 'MATCH' ? [] : [rule.value]), rule.target, ...(rule.options ?? [])].join(',')).join('\n')
}
export type RuleWorkspace = Pick<DeepReadonly<Workspace>, 'routes' | 'nodes' | 'policies' | 'subscriptions' | 'domainSets' | 'profiles' | 'drafts'>
export function targetCatalog(ws: RuleWorkspace, subscriptionId?: string) {
  const subscription = subscriptionId ? ws.subscriptions.find(s => s.id === subscriptionId) : undefined
  const issues: RuleIssue[] = []
  if (subscriptionId && !subscription) issues.push({ code: 'subscriptionMissing', detail: subscriptionId })
  const routeIds = subscription?.routeIds
  const routes = ws.routes.filter(route => (!routeIds || routeIds.includes(route.id)) && route.published && !route.withdrawnAt && route.cleanup !== 'confirmed')
  if (subscription) for (const id of subscription.routeIds) if (!routes.some(route => route.id === id)) issues.push({ code: 'routeMissing', detail: id })
  const names = new Set<string>(['DIRECT', 'REJECT'])
  const rows: { name: string; routeId?: string; routeName?: string }[] = [{ name: 'DIRECT' }, { name: 'REJECT' }]
  const add = (name: string, routeId?: string, routeName?: string) => { if (!names.has(name)) rows.push({ name, routeId, routeName }); names.add(name) }
  const byExit = subscription?.grouping?.byExit ?? true
  const allRoutes = subscription?.grouping?.allRoutes ?? true
  if (allRoutes && routes.length) add('All-Proxies')
  for (const route of routes) {
    const revision = route.published!
    const terminal = revision.nodeIds.at(-1)
    if (!terminal || revision.nodeIds.some(id => !ws.nodes.some(n => n.id === id && !n.removedAt))) { issues.push({ code: 'nodeMissing', detail: route.name, warning: !subscription }); continue }
    if (revision.policyId && !ws.policies.some(p => p.id === revision.policyId && p.revision === revision.policyRevision && p.terminalNodeId === terminal)) { issues.push({ code: 'policyMissing', detail: route.name, warning: !subscription }); continue }
    add(route.name, route.id, route.name)
    if (byExit) add(exitGroupName(revision), route.id, route.name)
  }
  // Explicit stored groups are checked against the selected, published route scope.
  const groupSubscriptions = subscription ? [subscription] : ws.subscriptions
  const groupNames = new Set<string>()
  for (const sub of groupSubscriptions) for (const group of sub.groups) {
    if (group.name === 'All-Proxies' && !allRoutes) continue
    if (group.name.startsWith('Exit-')) continue // canonical exit groups always use exitGroupName
    if (subscription && groupNames.has(group.name)) issues.push({ code: 'duplicateGroup', detail: group.name })
    groupNames.add(group.name)
    if (!group.routeIds.length || !group.routeIds.every(id => routes.some(r => r.id === id)) || !group.routeIds.includes(group.selectedRouteId)) { if (subscription) issues.push({ code: 'emptyGroup', detail: group.name }); continue }
    add(group.name)
  }
  if (subscription && (subscription.state === 'expired' || subscription.state === 'revoked' || Date.parse(subscription.expiresAt) <= Date.now())) issues.push({ code: 'subscriptionExpired', warning: true })
  return { names, rows, issues }
}
export type DnsMapping = { domain: string; suffix: boolean; target: string; source: string; line?: number }
export function analyzeRules(ws: RuleWorkspace, text: string, fallback: string, domainSetIds: readonly string[], subscriptionId?: string, chosenMatch?: number) {
  const parsed = parseRules(text), catalog = targetCatalog(ws, subscriptionId)
  const issues: RuleIssue[] = [...parsed.issues, ...catalog.issues], matches = parsed.lines.filter(line => line.type === 'MATCH')
  const selected = matches.length === 1 ? matches[0] : matches.find(line => line.line === chosenMatch)
  if (matches.length > 1 && !selected) matches.forEach(line => issues.push({ code: 'multipleMatch', line: line.line }))
  const finalTarget = selected?.target ?? fallback
  const ordered = parsed.lines.filter(line => line.type !== 'MATCH')
  const generated: RuleLine[] = [{ id: 'business-udp-guard', type: 'NETWORK', value: 'udp', target: 'REJECT' }, ...ordered.map(({ line: _line, ...rule }) => rule), { id: 'final-match', type: 'MATCH', value: '', target: finalTarget }]
  const mappings: DnsMapping[] = [], uninferred: ParsedLine[] = []
  const checkTarget = (target: string, line?: number) => { if (!catalog.names.has(target)) issues.push({ code: subscriptionId ? 'targetContext' : 'targetMissing', line, target }) }
  parsed.lines.forEach(line => checkTarget(line.target, line.line))
  if (!selected) checkTarget(finalTarget)
  const sets = domainSetIds.map(id => ws.domainSets.find(set => set.id === id))
  domainSetIds.forEach((id, i) => { if (!sets[i]) issues.push({ code: 'setMissing', detail: id }) })
  const usedSets = new Set<string>()
  const mapSet = (set: DeepReadonly<DomainSet>, line?: number) => {
    if (usedSets.has(set.id)) return
    usedSets.add(set.id); checkTarget(set.group, line)
    for (const domain of set.domains) {
      if (!isDomain(domain)) issues.push({ code: 'setDomain', line, detail: domain })
      else mappings.push({ domain: domain.toLowerCase(), suffix: true, target: set.group, source: set.identifier, line })
    }
    if (!set.domains.length) issues.push({ code: 'setEmpty', line, detail: set.identifier })
  }
  for (const rule of ordered) {
    if (['DOMAIN', 'DOMAIN-SUFFIX'].includes(rule.type)) mappings.push({ domain: rule.value.toLowerCase(), suffix: rule.type === 'DOMAIN-SUFFIX', target: rule.target, source: rule.type, line: rule.line })
    else if (rule.type === 'RULE-SET') {
      const candidates = sets.filter(set => set?.identifier === rule.value)
      const set = candidates[0]
      if (!set || candidates.length !== 1) issues.push({ code: 'setReference', line: rule.line, detail: rule.value })
      else { if (set.group !== rule.target) issues.push({ code: 'setTarget', line: rule.line, target: rule.target, detail: set.group }); mapSet(set, rule.line) }
    } else uninferred.push(rule)
  }
  // Attached sets are the reusable service bindings, also materialized as ordered RULE-SET rules.
  for (const set of sets) if (set && !usedSets.has(set.id)) {
    mapSet(set)
    generated.splice(generated.length - 1, 0, { id: `set-${set.id}`, type: 'RULE-SET', value: set.identifier, target: set.group })
  }
  for (let i = 0; i < mappings.length; i++) for (let j = i + 1; j < mappings.length; j++) {
    const a = mappings[i]!, b = mappings[j]!
    const overlaps = a.domain === b.domain || (a.suffix && b.domain.endsWith(`.${a.domain}`)) || (b.suffix && a.domain.endsWith(`.${b.domain}`))
    if (overlaps) issues.push({ code: a.target === b.target ? 'domainOverlap' : 'domainConflict', line: b.line, detail: `${a.domain} → ${a.target}; ${b.domain} → ${b.target}`, warning: a.target === b.target })
  }
  return { issues, matches, finalTarget, generated, mappings, uninferred, lines: parsed.lines, catalog, valid: !issues.some(issue => !issue.warning) }
}
export function profileReferences(ws: RuleWorkspace, id: string) { return ws.subscriptions.filter(sub => sub.profileId === id) }
export function setReferences(ws: RuleWorkspace, id: string) {
  const set = ws.domainSets.find(item => item.id === id)
  return { profiles: ws.profiles.filter(profile => profile.domainSetIds.includes(id) || (set && profile.rules.some(rule => rule.type === 'RULE-SET' && rule.value === set.identifier))), drafts: ws.drafts.filter(draft => draft.data?.profile && (draft.data.profile.domainSetIds.includes(id) || (set && parseRules(draft.data.profile.text).lines.some(rule => rule.type === 'RULE-SET' && rule.value === set.identifier)))) }
}
export function profileSource(profile: DeepReadonly<RuleProfile>) { return profile.sourceText ?? serializeRules(profile.rules) }
export function newId(prefix: string) { return `${prefix}-${crypto.randomUUID()}` }
