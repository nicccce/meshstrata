import type { DeepReadonly } from 'vue'
import type { Workspace, Event } from '../../domain'
import type { PageName } from '../../app/routes'

export type OverviewWorkspace = DeepReadonly<Workspace>
export type TodoCategory = 'publish' | 'offline' | 'expiry' | 'rollback' | 'cleanup' | 'sync' | 'failure'
export interface Destination { page: PageName; params?: Record<string, string>; query?: Record<string, string> }
export interface Todo {
  id: string; category: TodoCategory; name: string; objectId: string; updatedAt: string
  destination: Destination; taskId?: string; detail?: string
}
export function isPublished(route: OverviewWorkspace['routes'][number]) {
  // A failed attempt does not erase an earlier published revision.
  return !!route.published && !route.withdrawnAt
}
export function isEffective(subscription: OverviewWorkspace['subscriptions'][number], now: number) {
  return ['active', 'expiring'].includes(subscription.state) && Date.parse(subscription.expiresAt) > now
}
export function summarize(workspace: OverviewWorkspace, now: number) {
  return {
    nodes: workspace.nodes.filter(node => !node.removedAt).length,
    routes: workspace.routes.filter(isPublished).length,
    subscriptions: workspace.subscriptions.filter(subscription => isEffective(subscription, now)).length,
  }
}
export function onboarding(workspace: OverviewWorkspace): 'nodes' | 'routes' | 'subscriptions' | undefined {
  if (!workspace.nodes.some(node => !node.removedAt)) return 'nodes'
  if (!workspace.routes.some(isPublished)) return 'routes'
  if (!workspace.subscriptions.length) return 'subscriptions'
  return undefined
}
export function pendingItems(workspace: OverviewWorkspace, now: number): Todo[] {
  const result: Todo[] = []
  const add = (todo: Todo) => result.push(todo)
  const routeDestination = (id: string, tab = 'overview'): Destination => ({ page: 'route-detail', params: { routeId: id }, query: { tab } })
  for (const release of workspace.releases) {
    if (['rolling_back', 'rollback_pending', 'rollback_failed'].includes(release.state) || release.nodes.some(node => ['pending', 'failed'].includes(node.rollback))) {
      add({ id: `rollback-${release.id}`, category: 'rollback', name: release.name, objectId: release.id, updatedAt: release.updatedAt, taskId: release.id,
        detail: release.state, destination: { page: 'release-detail', params: { releaseId: release.id } } })
    }
    if (release.cleanup === 'pending') add({ id: `cleanup-${release.id}`, category: 'cleanup', name: release.name, objectId: release.id,
      updatedAt: release.updatedAt, taskId: release.id, destination: { page: 'release-detail', params: { releaseId: release.id } } })
    if (release.state === 'failed' && !release.nodes.some(node => ['pending', 'failed'].includes(node.rollback))) add({ id: `failed-${release.id}`, category: 'failure', name: release.name, objectId: release.id,
      updatedAt: release.updatedAt, taskId: release.id, destination: { page: 'release-detail', params: { releaseId: release.id } } })
  }
  for (const route of workspace.routes) {
    const tasks = workspace.releases.filter(release => release.routeId === route.id)
    const savedDraft = [...workspace.drafts]
      .filter(draft => draft.objectId === route.id && ['route', 'routes', 'route-editor'].includes(draft.feature))
      .sort((a, b) => (Date.parse(b.updatedAt) || 0) - (Date.parse(a.updatedAt) || 0))[0]
    const draftStep = savedDraft?.step && ['exit', 'paths', 'options', 'review'].includes(savedDraft.step) ? savedDraft.step : 'review'
    if (!route.withdrawnAt && (route.state === 'draft' || (route.published && route.draft.revision > route.published.revision))) add({ id: `publish-${route.id}`, category: 'publish', name: route.name, objectId: route.id,
      updatedAt: savedDraft?.updatedAt ?? route.updatedAt, destination: { page: 'route-edit', params: { routeId: route.id }, query: savedDraft ? { draftId: savedDraft.id, step: draftStep } : { step: 'review' } } })
    if (route.cleanup === 'pending' && !tasks.some(task => task.cleanup === 'pending')) add({ id: `route-cleanup-${route.id}`, category: 'cleanup', name: route.name, objectId: route.id,
      updatedAt: route.updatedAt, destination: routeDestination(route.id, 'resources') })
    if (['rolling_back', 'rollback_pending', 'rollback_failed'].includes(route.state) && !tasks.some(task => ['rolling_back', 'rollback_pending', 'rollback_failed'].includes(task.state) || task.nodes.some(node => ['pending', 'failed'].includes(node.rollback)))) add({ id: `route-rollback-${route.id}`, category: 'rollback', name: route.name, objectId: route.id,
      updatedAt: route.updatedAt, destination: routeDestination(route.id, 'history') })
    if (route.state === 'failed' && !tasks.length) add({ id: `route-failure-${route.id}`, category: 'failure', name: route.name, objectId: route.id,
      updatedAt: route.updatedAt, destination: routeDestination(route.id, 'history') })
  }
  for (const draft of workspace.drafts) {
    // Do not duplicate the route object's pending publication. Other feature drafts
    // remain in their own workflows; their private editor semantics are not guessed.
    if (draft.feature !== 'route' && draft.feature !== 'routes' && draft.feature !== 'route-editor') continue
    if (draft.objectId && workspace.routes.some(route => route.id === draft.objectId && route.withdrawnAt)) continue
    if (draft.objectId && workspace.routes.some(route => route.id === draft.objectId && result.some(todo => todo.id === `publish-${route.id}`))) continue
    const step = draft.step && ['exit', 'paths', 'options', 'review'].includes(draft.step) ? draft.step : 'review'
    add({ id: `draft-${draft.id}`, category: 'publish', name: draft.name, objectId: draft.id, updatedAt: draft.updatedAt,
      destination: draft.objectId ? { page: 'route-edit', params: { routeId: draft.objectId }, query: { draftId: draft.id, step } } : { page: 'route-new', query: { draftId: draft.id, step } } })
  }
  for (const listener of workspace.listeners.filter(listener => listener.cleanup === 'pending')) {
    const covered = listener.routeIds.some(id => workspace.routes.some(route => route.id === id && route.cleanup === 'pending') || workspace.releases.some(release => release.routeId === id && release.cleanup === 'pending'))
    if (covered) continue
    const routeId = listener.routeIds.find(id => workspace.routes.some(route => route.id === id))
    add({ id: `listener-cleanup-${listener.id}`, category: 'cleanup', name: listener.name, objectId: listener.id, updatedAt: listener.updatedAt,
      destination: routeId ? routeDestination(routeId, 'resources') : { page: 'node-detail', params: { nodeId: listener.nodeId }, query: { tab: 'protocols' } } })
  }
  for (const node of workspace.nodes.filter(node => !node.removedAt && (node.control === 'offline' || (node.mode === 'reverse' && node.tunnel === 'offline')))) {
    add({ id: `offline-${node.id}`, category: 'offline', name: node.name, objectId: node.id, updatedAt: node.updatedAt,
      destination: { page: 'node-detail', params: { nodeId: node.id }, query: { tab: 'connectivity' } } })
  }
  for (const subscription of workspace.subscriptions) {
    const entries = workspace.entries.filter(entry => entry.subscriptionId === subscription.id)
    const expired = subscription.state === 'expired' || Date.parse(subscription.expiresAt) <= now
    const revocationPending = (expired || subscription.state === 'revoked') && (subscription.sync === 'pending' || entries.some(entry => entry.identityState !== 'revoked'))
    const expiring = isEffective(subscription, now) && (subscription.state === 'expiring' || Date.parse(subscription.expiresAt) - now <= 7 * 86400000)
    if (revocationPending || expiring) add({ id: `expiry-${subscription.id}`, category: 'expiry', name: subscription.name, objectId: subscription.id,
      updatedAt: subscription.expiresAt, detail: revocationPending ? 'revoke' : 'expiring', destination: { page: 'subscription-detail', params: { subscriptionId: subscription.id }, query: { tab: revocationPending ? 'entries' : 'overview' } } })
    if (subscription.sync === 'pending' && !revocationPending) add({ id: `sync-${subscription.id}`, category: 'sync', name: subscription.name, objectId: subscription.id,
      updatedAt: subscription.updatedAt, destination: { page: 'subscription-detail', params: { subscriptionId: subscription.id }, query: { tab: 'entries' } } })
    if (entries.some(entry => entry.cleanup === 'pending') && !workspace.releases.some(release => release.subscriptionId === subscription.id && release.cleanup === 'pending')) add({ id: `entries-cleanup-${subscription.id}`, category: 'cleanup', name: subscription.name, objectId: subscription.id,
      updatedAt: subscription.updatedAt, destination: { page: 'subscription-detail', params: { subscriptionId: subscription.id }, query: { tab: 'entries' } } })
  }
  const priority: TodoCategory[] = ['rollback', 'failure', 'expiry', 'cleanup', 'offline', 'sync', 'publish']
  return result.sort((a, b) => priority.indexOf(a.category) - priority.indexOf(b.category) || (Date.parse(b.updatedAt) || 0) - (Date.parse(a.updatedAt) || 0) || a.id.localeCompare(b.id))
}
export function eventObject(workspace: OverviewWorkspace, event: DeepReadonly<Event>) {
  const collection = { node: workspace.nodes, route: workspace.routes, subscription: workspace.subscriptions, release: workspace.releases }[event.entityType]
  return collection.find(entity => entity.id === event.entityId)
}
export function observationExpired(observedAt: string | undefined, ttlHours: number, now: number) {
  return !!observedAt && (!Number.isFinite(Date.parse(observedAt)) || Date.parse(observedAt) + ttlHours * 3600000 <= now)
}
