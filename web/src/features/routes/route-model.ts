import type { DeepReadonly } from 'vue'
import type { Draft, Route, RouteRevision, Workspace } from '../../domain'

export type Snapshot = DeepReadonly<Workspace>
export type RouteSnapshot = DeepReadonly<Route>
export type RevisionSnapshot = DeepReadonly<RouteRevision>
export const protocols = ['ss2022', 'trojan', 'vless', 'hysteria2'] as const
export const states = ['draft', 'validating', 'applying', 'verifying', 'published', 'failed', 'rolling_back', 'rolled_back', 'rollback_pending', 'rollback_failed', 'withdrawn'] as const
export const udpStates = ['not_checked', 'not_supported', 'incomplete'] as const
export function clone<T>(value: DeepReadonly<T>): T { return JSON.parse(JSON.stringify(value)) }
export function displayedRevision(route: RouteSnapshot) { return route.published ?? route.draft }
export function configState(route: RouteSnapshot) { return route.withdrawnAt ? 'withdrawn' : route.state }
export function savedRouteDraft(ws: Snapshot, routeId: string) {
  return ws.drafts.filter(draft => ['route', 'routes', 'route-editor'].includes(draft.feature) && draft.objectId === routeId && draft.data?.route).slice().sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))[0]
}
export function editorRevision(ws: Snapshot, route: RouteSnapshot) {
  const paths = savedRouteDraft(ws, route.id)?.data?.route?.selectedPaths
  return paths?.find(path => path.id === route.id)?.revision ?? paths?.[0]?.revision ?? route.draft
}
export function revisionChanged(route: RouteSnapshot, draftRevision: RevisionSnapshot = route.draft) {
  if (!route.published) return true
  const { validated: _draftValidation, revision: _draftNumber, ...draft } = clone<RouteRevision>(draftRevision)
  const { validated: _publishedValidation, revision: _publishedNumber, ...published } = clone<RouteRevision>(route.published)
  return JSON.stringify(draft) !== JSON.stringify(published) || draftRevision.revision !== route.published.revision
}
export function nodeName(ws: Snapshot, id: string) { return ws.nodes.find(node => node.id === id)?.name ?? id }
export function pathNodes(ws: Snapshot, revision: RevisionSnapshot) {
  return revision.nodeIds.map(id => ({ id, name: nodeName(ws, id) }))
}
export function routeProtocols(ws: Snapshot, revision: RevisionSnapshot) {
  const entry = ws.listeners.find(listener => listener.id === revision.entryListenerId)
  return [...new Set([...(entry ? [entry.protocol] : []), ...revision.links.map(link => link.protocol)])]
}
// Configured relay support is distinct from an observation. HTTP checks never verify UDP.
export function udpCapability(ws: Snapshot, revision: RevisionSnapshot): typeof udpStates[number] {
  const listeners = [revision.entryListenerId, ...revision.links.map(link => link.listenerId)].map(id => ws.listeners.find(listener => listener.id === id))
  if (revision.nodeIds.some(id => ws.nodes.find(node => node.id === id)?.mode === 'reverse') || revision.links.some(link => link.tunnelId) || listeners.some(listener => listener?.transport === 'tcp')) return 'not_supported'
  if (!revision.nodeIds.length || revision.nodeIds.some(id => !ws.nodes.some(node => node.id === id)) || listeners.some(listener => !listener)) return 'incomplete'
  if (revision.policyId) {
    const policy = ws.policies.find(item => item.id === revision.policyId && item.revision === revision.policyRevision)
    if (!policy) return 'incomplete'
    const targets = policy.rules.filter(rule => rule.targetId !== 'REJECT').map(rule => ws.targets.find(target => target.id === rule.targetId))
    if (targets.some(target => target?.type === 'http')) return 'not_supported'
    if (targets.some(target => !target)) return 'incomplete'
  }
  return 'not_checked'
}
export function referencedSubscriptions(ws: Snapshot, routeId: string) {
  return ws.subscriptions.filter(subscription => subscription.routeIds.includes(routeId) || ws.entries.some(entry => entry.routeId === routeId && entry.subscriptionId === subscription.id))
}
export function routeResources(ws: Snapshot, route: RouteSnapshot) {
  const revisions = [route.draft, editorRevision(ws, route), ...(route.published ? [route.published] : [])]
  const listenerIds = new Set(revisions.flatMap(revision => [revision.entryListenerId, ...revision.links.map(link => link.listenerId)]))
  const tunnelIds = new Set(revisions.flatMap(revision => revision.links.flatMap(link => link.tunnelId ? [link.tunnelId] : [])))
  return {
    listeners: ws.listeners.filter(listener => listenerIds.has(listener.id) || listener.routeIds.includes(route.id) || ws.entries.some(entry => entry.routeId === route.id && entry.listenerId === listener.id)),
    tunnels: ws.tunnels.filter(tunnel => tunnelIds.has(tunnel.id) || tunnel.routeIds.includes(route.id)),
    missingListeners: [...listenerIds].filter(id => !ws.listeners.some(listener => listener.id === id)),
    missingTunnels: [...tunnelIds].filter(id => !ws.tunnels.some(tunnel => tunnel.id === id)),
  }
}
export function canUseRoute(ws: Snapshot, route: RouteSnapshot) {
  if (!route.published || route.withdrawnAt) return false
  const rev = route.published
  const policy = rev.policyId ? ws.policies.find(policy => policy.id === rev.policyId && policy.revision === rev.policyRevision) : undefined
  return rev.nodeIds.length > 0 && rev.nodeIds.every(id => ws.nodes.some(node => node.id === id && !node.removedAt)) && [rev.entryListenerId, ...rev.links.map(link => link.listenerId)].every(id => ws.listeners.some(listener => listener.id === id)) && rev.links.every(link => !link.tunnelId || ws.tunnels.some(tunnel => tunnel.id === link.tunnelId && tunnel.revision === link.tunnelRevision)) && (!rev.policyId || (!!policy && policy.terminalNodeId === rev.nodeIds.at(-1) && policy.rules.every(rule => rule.targetId === 'REJECT' || ws.targets.some(target => target.id === rule.targetId && target.nodeId === policy.terminalNodeId))))
}
export function duplicateDraft(ws: Snapshot, source: RouteSnapshot, id: string, name: string): Draft {
  const revision = clone<RouteRevision>(displayedRevision(source))
  revision.revision = 1
  revision.validated = false
  return {
    id, feature: 'route', name, nodeIds: [...revision.nodeIds], routeIds: [], step: 'options', updatedAt: new Date().toISOString(),
    data: { route: {
      exitNodeId: revision.nodeIds.at(-1) ?? '', environment: revision.environment ?? ws.settings.environment ?? 'mainland_china',
      allowUdpTransport: revision.allowUdpTransport ?? ws.settings.allowUdpTransport ?? false,
      requireUdpRelay: revision.requireUdpRelay ?? ws.settings.requireUdpRelay ?? false,
      currentPathId: id, selectedPaths: [{ id, name, revision }],
    } },
  }
}
