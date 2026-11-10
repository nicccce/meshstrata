import type { DeepReadonly } from 'vue'
import type { Event, Release, ReleaseStage, Workspace } from '../../domain'

type Snapshot = DeepReadonly<Workspace>
export type Source = { kind: Event['entityType']; id: string }
export type ReleaseAction = 'retry' | 'rollback' | 'cleanup' | 'cancel'
export type PlanStep = { id: string; key: string; nodeId?: string }

export function releaseSources(release: DeepReadonly<Release>): Source[] {
  const sources: Source[] = []
  if (release.subscriptionId) sources.push({ kind: 'subscription', id: release.subscriptionId })
  if (release.routeId) sources.push({ kind: 'route', id: release.routeId })
  if (release.nodeId) sources.push({ kind: 'node', id: release.nodeId })
  return sources.length ? sources : [{ kind: 'release', id: release.id }]
}

export function sourceName(ws: Snapshot, source: Source): string | undefined {
  const collection = { node: ws.nodes, route: ws.routes, subscription: ws.subscriptions, release: ws.releases }[source.kind]
  return collection.find(item => item.id === source.id)?.name
}

export function releaseNodeIds(release: DeepReadonly<Release>, ws: Snapshot): string[] {
  const ids = release.nodes.map(node => node.nodeId)
  if (release.nodeId) ids.push(release.nodeId)
  if (release.routeId) {
    const route = ws.routes.find(route => route.id === release.routeId)
    const revision = route?.draft.revision === release.revision ? route.draft : route?.published?.revision === release.revision ? route.published : undefined
    if (revision) ids.push(...revision.nodeIds)
  }
  if (release.subscriptionId) {
    for (const entry of ws.entries.filter(entry => entry.subscriptionId === release.subscriptionId)) {
      const listener = ws.listeners.find(listener => listener.id === entry.listenerId)
      if (listener) ids.push(listener.nodeId)
    }
  }
  return [...new Set(ids)]
}

export function rollbackMode(release: DeepReadonly<Release>): boolean {
  return release.kind === 'rollback' || ['rolling_back', 'rollback_pending', 'rollback_failed', 'rolled_back'].includes(release.state) || release.nodes.some(node => node.rollback !== 'none')
}

export function dependencyPlan(release: DeepReadonly<Release>, ws: Snapshot): PlanStep[] {
  const ids = releaseNodeIds(release, ws)
  const route = ws.routes.find(route => route.id === release.routeId)
  const revision = route?.draft.revision === release.revision ? route.draft : route?.published?.revision === release.revision ? route.published : undefined
  const reverse = ids.find(id => ws.nodes.find(node => node.id === id)?.mode === 'reverse')
  const relay = reverse ? ws.tunnels.find(tunnel => tunnel.remoteNodeId === reverse && (!release.routeId || tunnel.routeIds.includes(release.routeId)))?.relayNodeId ?? ws.settings.relayNodeId : undefined
  const result: PlanStep[] = [{ id: 'save', key: 'save' }, { id: 'validate', key: 'validate' }]
  if (reverse) {
    result.push(
      { id: 'remote-listener', key: 'remoteListener', nodeId: reverse },
      { id: 'relay-authorization', key: 'relayAuthorization', nodeId: relay },
      { id: 'tunnel-mapping', key: 'tunnelMapping', nodeId: reverse },
      { id: 'mapping-auth', key: 'mappingAuth', nodeId: relay },
      { id: 'entry', key: 'entry', nodeId: relay },
    )
  } else {
    const order = revision?.nodeIds ?? ids
    for (const nodeId of [...order.slice(1)].reverse()) result.push({ id: `downstream-${nodeId}`, key: 'downstream', nodeId })
    if (order[0]) result.push({ id: 'entry', key: 'entry', nodeId: order[0] })
  }
  result.push({ id: 'listeners-auth', key: 'listenerAuth' }, { id: 'route-verify', key: 'routeVerify' }, { id: 'confirm', key: 'confirm' })
  return result
}

// Destructive resource deletion needs exact release-to-resource attribution. The
// shared model does not provide that mapping. Retain every port and reference.
export function cleanupBlockers(release: DeepReadonly<Release>, ws: Snapshot): string[] {
  const refs: string[] = []
  if (release.routeId) {
    refs.push(...ws.listeners.filter(listener => listener.routeIds.includes(release.routeId!)).map(listener => listener.id))
    refs.push(...ws.tunnels.filter(tunnel => tunnel.routeIds.includes(release.routeId!)).map(tunnel => tunnel.id))
  }
  if (release.subscriptionId) refs.push(...ws.entries.filter(entry => entry.subscriptionId === release.subscriptionId && entry.cleanup !== 'confirmed').map(entry => entry.id))
  return [...new Set(refs)]
}

export function canCancel(release: DeepReadonly<Release>): boolean {
  return ['draft', 'validating'].includes(release.state) && release.cleanup === 'none' && release.nodes.every(node => !node.applied && !node.verified && node.rollback === 'none') && !(release.stages ?? []).some(stage => stage.state === 'running' || stage.state === 'succeeded' && !['save', 'validate'].includes(stage.id))
}

export function canRetry(release: DeepReadonly<Release>): boolean {
  if (release.issue?.code === 'features.activity.issues.CANCELLED') return false
  if (release.state === 'published') return !release.nodes.length || release.nodes.some(node => !node.applied || !node.verified) || !!release.stages?.some(stage => stage.state !== 'succeeded')
  return !['rolled_back', 'draft'].includes(release.state)
}

/** Pure, deterministic demo result. It never changes source revisions, identities,
 * reference arrays, Listener ports, tunnel mappings or expired/revoked states. */
export function simulateReleaseAttempt(original: DeepReadonly<Release>, ws: Snapshot, action: ReleaseAction, now: string): Release {
  const result: Release = JSON.parse(JSON.stringify(original))
  result.updatedAt = now
  result.startedAt ??= now
  delete result.completedAt
  delete result.issue
  if (action === 'cancel') {
    if (!canCancel(original)) return result
    // ReleaseState has no cancelled state. A pre-apply cancellation is represented
    // as a failed task with a precise issue, rather than fake rollback success.
    result.state = 'failed'
    result.issue = { code: 'features.activity.issues.CANCELLED' }
    result.completedAt = now
    return result
  }
  const rollback = action === 'rollback' || rollbackMode(original)
  const cleanup = action === 'cleanup' || original.kind === 'cleanup'
  const ids = releaseNodeIds(original, ws)
  if (cleanup) {
    const available = ids.length > 0 && ids.every(id => ws.nodes.some(node => node.id === id && node.control === 'online' && !node.removedAt))
    const retained = cleanupBlockers(original, ws).length > 0
    result.cleanup = available && !retained && ws.scenario !== 'error' && ws.scenario !== 'partial' ? 'confirmed' : 'pending'
    if (result.cleanup === 'pending') result.issue = { code: retained ? 'features.activity.issues.REFERENCES_RETAINED' : !available ? 'NODE_OFFLINE' : 'DEMO_ERROR' }
    // Cleanup is independent from publishing/rollback. Do not rewrite the
    // application outcome or grant new runtime confirmations while cleaning up.
    result.completedAt = original.completedAt
    return result
  }
  const sourceMissing = releaseSources(original).some(source => source.kind !== 'release' && !sourceName(ws, source))
  const sourceRoute = ws.routes.find(route => route.id === original.routeId)
  const wrongRevision = !rollback && original.kind === 'publish' && !!original.routeId && sourceRoute?.draft.revision !== original.revision && sourceRoute?.published?.revision !== original.revision
  if (sourceMissing || wrongRevision) {
    result.state = rollback ? 'rollback_pending' : 'failed'
    result.issue = { code: wrongRevision ? 'features.activity.issues.REVISION_UNAVAILABLE' : 'features.activity.issues.SOURCE_MISSING' }
    return result
  }
  result.nodes = ids.map(nodeId => {
    const old = original.nodes.find(node => node.nodeId === nodeId)
    return old ? JSON.parse(JSON.stringify(old)) : { nodeId, applied: false, verified: false, rollback: 'none' }
  })
  if (!ids.length) {
    result.state = 'failed'
    result.issue = { code: 'features.activity.issues.NO_SCOPE' }
    return result
  }
  let failed = !!original.stages?.some(stage => stage.nodeId && !ids.includes(stage.nodeId))
  let blocked = false
  // Application follows downstream -> entry, or the Reverse dependency order.
  const order = [...new Set(dependencyPlan(original, ws).flatMap(step => step.nodeId ? [step.nodeId] : [])), ...ids]
  const visited = new Set<string>()
  for (const nodeId of order) {
    if (visited.has(nodeId)) continue
    visited.add(nodeId)
    const record = result.nodes.find(node => node.nodeId === nodeId)
    if (!record) continue
    const node = ws.nodes.find(node => node.id === nodeId)
    const unavailable = !node || node.control !== 'online' || !!node.removedAt
    const alreadyConfirmed = rollback ? record.rollback === 'confirmed' : record.applied && record.verified && record.rollback === 'none'
    // Previous confirmations remain historical; offline is still a blocker for
    // this attempt. Never generate a new confirmation for an offline node.
    if (unavailable) {
      if (!alreadyConfirmed) {
        if (rollback) record.rollback = 'pending'
        record.issue = { code: 'NODE_OFFLINE' }
      }
      failed = true
      blocked = true
      continue
    }
    if (alreadyConfirmed && !cleanup) continue
    const demoFailure = ws.scenario === 'error' || ws.scenario === 'partial' && nodeId === ids.at(-1)
    const tunnelUnavailable = !rollback && !cleanup && original.kind === 'publish' && node.mode === 'reverse' && node.tunnel !== 'online'
    if (demoFailure || tunnelUnavailable || blocked && !rollback && !cleanup) {
      record.issue = { code: demoFailure ? 'DEMO_ERROR' : tunnelUnavailable ? 'features.activity.issues.TUNNEL_UNAVAILABLE' : 'features.activity.issues.DEPENDENCY_PENDING' }
      if (rollback) record.rollback = 'failed'
      failed = true
      blocked = true
      continue
    }
    delete record.issue
    if (rollback) { record.rollback = 'confirmed'; record.verified = false }
    else { record.applied = true; record.verified = true }
  }
  const sourcesMissing = releaseSources(original).some(source => source.kind !== 'release' && !sourceName(ws, source))
  const route = ws.routes.find(route => route.id === original.routeId)
  const revisionMissing = original.kind === 'publish' && !!original.routeId && route?.draft.revision !== original.revision && route?.published?.revision !== original.revision
  if (sourcesMissing || revisionMissing) {
    failed = true
    result.issue = { code: revisionMissing ? 'features.activity.issues.REVISION_UNAVAILABLE' : 'features.activity.issues.SOURCE_MISSING' }
  }
  if (cleanup && cleanupBlockers(original, ws).length) {
    result.cleanup = 'pending'
    result.issue = { code: 'features.activity.issues.REFERENCES_RETAINED' }
    failed = true
  } else if (cleanup) result.cleanup = failed ? 'pending' : 'confirmed'
  if (rollback) result.state = failed ? result.nodes.some(node => node.rollback === 'failed') ? 'rollback_failed' : 'rollback_pending' : 'rolled_back'
  else result.state = failed ? 'failed' : 'published'
  if (original.stages?.length && !rollback && !cleanup) {
    result.stages = original.stages.map((stage): ReleaseStage => {
      if (stage.state === 'succeeded') return JSON.parse(JSON.stringify(stage))
      const node = stage.nodeId ? result.nodes.find(node => node.nodeId === stage.nodeId) : undefined
      const success = !failed && (!stage.nodeId || !!node?.verified)
      return { ...stage, state: success ? 'succeeded' : 'failed', issue: success ? undefined : node?.issue ?? result.issue ?? { code: 'features.activity.issues.DEPENDENCY_PENDING' }, completedAt: success ? now : undefined }
    })
  }
  if (!failed) result.completedAt = now
  return result
}

export function tailText(lines: readonly string[], lineLimit: number, byteLimit: number): { text: string; bytes: number; lines: number; truncated: boolean } {
  const selected: string[] = []
  const encoder = new TextEncoder()
  let bytes = 0
  for (const line of [...lines].reverse().slice(0, lineLimit)) {
    const length = encoder.encode(line).byteLength + (selected.length ? 1 : 0)
    if (bytes + length > byteLimit) break
    selected.unshift(line)
    bytes += length
  }
  return { text: selected.join('\n'), bytes, lines: selected.length, truncated: selected.length < lines.length }
}
