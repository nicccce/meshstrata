import type { Draft, Event, Listener, Release, Subscription, SubscriptionEntry } from '../../domain'
import { commands, workspace, sessionSecrets } from '../../demo/workspace'
import { buildPlan, clone, draftFor, effectiveState, id, type Plan } from './model'

function record(sub: Subscription, kind: 'publish' | 'revoke' | 'cleanup', nodeIds: string[], cleanup = false) {
  const now = new Date().toISOString()
  const nodes = [...new Set(nodeIds)].map(nodeId => {
    const online = workspace.nodes.find(n => n.id === nodeId)?.control === 'online'
    return { nodeId, applied: online, verified: online, rollback: 'none' as const }
  })
  const pending = nodes.some(n => !n.applied)
  const release: Release = { id: id('release-sub'), name: `${sub.name} · simulated ${kind}`, updatedAt: now, subscriptionId: sub.id, kind, state: pending ? 'applying' : 'published', revision: sub.downloadVersion ?? 1, nodes, cleanup: cleanup ? (pending ? 'pending' : 'confirmed') : 'none', startedAt: now, ...(pending ? {} : { completedAt: now }) }
  commands.upsert('releases', release)
  const event: Event = { id: id('event-sub'), name: release.name, updatedAt: now, kind, entityType: 'subscription', entityId: sub.id, status: pending ? 'pending' : 'simulated', releaseId: release.id }
  commands.upsert('events', event)
}
function online(entry: SubscriptionEntry) {
  const listener = workspace.listeners.find(l => l.id === entry.listenerId)
  return !!listener && workspace.nodes.find(n => n.id === listener.nodeId)?.control === 'online'
}
function releaseListener(entry: SubscriptionEntry) {
  const listener = workspace.listeners.find(l => l.id === entry.listenerId)
  if (!listener) return
  const shared = workspace.entries.some(e => e.id !== entry.id && e.listenerId === listener.id && e.cleanup !== 'confirmed') || workspace.routes.some(r => r.published?.entryListenerId === listener.id || r.published?.links.some(l => l.listenerId === listener.id))
  const changed = clone<Listener>(listener)
  changed.entryIds = (changed.entryIds ?? []).filter(e => e !== entry.id)
  if (!shared) changed.cleanup = entry.cleanup === 'confirmed' ? 'confirmed' : 'pending'
  commands.upsert('listeners', changed)
}
function revoke(entry: SubscriptionEntry) {
  entry.identityState = online(entry) ? 'revoked' : 'pending'
  entry.cleanup = online(entry) ? 'confirmed' : 'pending'
  entry.updatedAt = new Date().toISOString()
  commands.upsert('entries', entry); releaseListener(entry)
  sessionSecrets.delete(`subscription-entry:${entry.id}`)
}
export function saveSubscription(draft: Draft, subscriptionId: string): { plan: Plan; saved?: Subscription } {
  // Recompute immediately before write; another page may have reserved a preview port.
  const plan = buildPlan(workspace, draft, subscriptionId)
  if (plan.errors.length || !draft.data?.subscription) return { plan }
  const form = draft.data.subscription
  const previous = workspace.subscriptions.find(s => s.id === subscriptionId)
  if (previous?.state === 'revoked') return { plan }
  const now = new Date().toISOString()
  for (const entry of workspace.entries.filter(e => e.subscriptionId === subscriptionId && !draft.routeIds.includes(e.routeId) && e.cleanup !== 'confirmed')) revoke(clone<SubscriptionEntry>(entry))
  let pending = false
  const nodeIds: string[] = []
  for (const p of plan.entries) {
    nodeIds.push(p.listener.nodeId)
    const isOnline = workspace.nodes.find(n => n.id === p.listener.nodeId)?.control === 'online'
    p.entry.expiresAt = form.expiresAt
    p.entry.identityState = isOnline ? 'active' : 'pending'
    p.entry.updatedAt = now; p.entry.cleanup = 'none'
    pending ||= !isOnline
    commands.upsert('listeners', p.listener); commands.upsert('entries', p.entry)
    // Random illustrative proxy identities are memory-only and never enter exports.
    if (!p.reused && !sessionSecrets.has(`subscription-entry:${p.entry.id}`)) sessionSecrets.set(`subscription-entry:${p.entry.id}`, crypto.randomUUID())
  }
  pending ||= workspace.entries.some(e => e.subscriptionId === subscriptionId && e.cleanup === 'pending')
  const sub: Subscription = { id: subscriptionId, name: draft.name.trim(), updatedAt: now, routeIds: [...draft.routeIds], profileId: draft.profileId!, entryIds: plan.entries.map(p => p.entry.id), expiresAt: form.expiresAt, state: 'active', sync: pending ? 'pending' : 'synced', groups: plan.groups, description: form.description.trim(), grouping: clone(form.grouping), client: clone(form.client), downloadVersion: previous?.downloadVersion ?? 1 }
  sub.state = effectiveState(sub)
  commands.upsert('subscriptions', sub)
  record(sub, 'publish', nodeIds)
  return { plan, saved: sub }
}
export function deleteSubscription(source: Subscription) {
  const sub = clone<Subscription>(source)
  const entries = workspace.entries.filter(e => e.subscriptionId === sub.id && e.cleanup !== 'confirmed').map(e => clone<SubscriptionEntry>(e))
  const nodeIds = entries.flatMap(e => { const l = workspace.listeners.find(l => l.id === e.listenerId); return l ? [l.nodeId] : [] })
  entries.forEach(revoke)
  sub.state = 'revoked'; sub.sync = entries.some(e => e.identityState === 'pending') ? 'pending' : 'synced'; sub.updatedAt = new Date().toISOString()
  sub.downloadVersion = (sub.downloadVersion ?? 1) + 1
  commands.upsert('subscriptions', sub)
  for (const d of workspace.drafts.filter(d => d.feature === 'subscriptions' && d.objectId === sub.id)) commands.remove('drafts', d.id)
  record(sub, 'revoke', nodeIds, true)
}
export function extendSubscription(source: Subscription, expiresAt: string) {
  if (source.state === 'revoked' || Date.parse(expiresAt) <= Math.max(Date.now(), Date.parse(source.expiresAt))) return false
  const draft = draftFor(source)
  draft.data!.subscription!.expiresAt = expiresAt
  // Renewal goes through the same isolation and allocation checks as editing.
  return !!saveSubscription(draft, source.id).saved
}
export function syncSubscription(source: Subscription) {
  const sub = clone<Subscription>(source); const nodeIds: string[] = []
  for (const value of workspace.entries.filter(e => e.subscriptionId === sub.id)) {
    const entry = clone<SubscriptionEntry>(value)
    if (!online(entry)) continue
    const listener = workspace.listeners.find(l => l.id === entry.listenerId)
    if (listener) nodeIds.push(listener.nodeId)
    const removed = !sub.entryIds.includes(entry.id)
    const revoked = removed || effectiveState(sub) === 'expired' || sub.state === 'revoked'
    entry.identityState = revoked ? 'revoked' : 'active'; entry.cleanup = removed || sub.state === 'revoked' ? 'confirmed' : 'none'; entry.updatedAt = new Date().toISOString()
    commands.upsert('entries', entry)
    if (entry.cleanup === 'confirmed') releaseListener(entry)
  }
  if (!nodeIds.length) return false
  sub.sync = workspace.entries.some(e => e.subscriptionId === sub.id && (e.identityState === 'pending' || e.cleanup === 'pending')) ? 'pending' : 'synced'
  sub.updatedAt = new Date().toISOString(); commands.upsert('subscriptions', sub)
  // Finish only this subscription's local pending lifecycle records.
  for (const value of workspace.releases.filter(r => r.subscriptionId === sub.id && r.state === 'applying')) {
    const release = clone<Release>(value)
    release.nodes.forEach(n => { if (nodeIds.includes(n.nodeId)) { n.applied = true; n.verified = true } })
    if (release.nodes.every(n => n.applied)) { release.state = 'published'; release.completedAt = sub.updatedAt; if (release.cleanup === 'pending') release.cleanup = 'confirmed' }
    commands.upsert('releases', release)
  }
  record(sub, 'cleanup', nodeIds, true)
  return true
}
