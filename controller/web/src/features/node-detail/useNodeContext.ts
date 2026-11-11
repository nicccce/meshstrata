import { computed, watch } from 'vue'
import { onBeforeRouteUpdate } from 'vue-router'
import { useWorkspace, resetEpoch } from '../../demo/workspace'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
import type { Release, ReleaseStage } from '../../domain'
import { relatedRoutes, issue } from './model'

export function useNodeContext(nodeId: string) {
  const { workspace, commands } = useWorkspace()
  const node = computed(() => workspace.nodes.find(item => item.id === nodeId)!)
  const routes = computed(() => relatedRoutes(workspace, nodeId))
  const listeners = computed(() => workspace.listeners.filter(item => item.nodeId === nodeId))
  const tunnels = computed(() => workspace.tunnels.filter(item => item.remoteNodeId === nodeId || item.relayNodeId === nodeId))
  return { workspace, commands, node, routes, listeners, tunnels }
}

export function useNodeTask() {
  const task = useSimulatedTask()
  watch(resetEpoch, task.cancel)
  onBeforeRouteUpdate((to, from) => { if (to.params.nodeId !== from.params.nodeId) task.cancel() })
  return task
}

export function useNodeRelease(nodeId: string) {
  const { workspace, commands } = useWorkspace()
  const task = useNodeTask()
  async function publish(name: string, revision: number, apply: () => boolean | void, stageNames: string[]) {
    if (task.state.value === 'running') return
    const node = workspace.nodes.find(n => n.id === nodeId)
    if (!node || node.removedAt) return
    const epoch = resetEpoch.value
    const now = new Date().toISOString()
    const id = `node-${nodeId}-${Date.now()}-${workspace.releases.length}`
    const stages: ReleaseStage[] = stageNames.map((name, i) => ({ id: `${id}-${i}`, name, nodeId, state: i === 0 ? 'running' : 'pending' }))
    const release: Release = { id, name, kind: 'publish', nodeId, revision, state: 'validating', updatedAt: now, startedAt: now,
      nodes: [{ nodeId, applied: false, verified: false, rollback: 'none' }], stages, cleanup: 'none' }
    commands.upsert('releases', release)
    const failed = workspace.scenario === 'error' || node.control !== 'online'
    const success = await task.run(failed ? 'error' : 'success')
    if (epoch !== resetEpoch.value) return
    const completedAt = new Date().toISOString()
    release.completedAt = completedAt
    release.updatedAt = completedAt
    const currentNode = workspace.nodes.find(n => n.id === nodeId)
    const applied = success && currentNode && !currentNode.removedAt && apply() !== false
    if (applied) {
      release.state = 'published'
      release.nodes[0]!.applied = true
      release.nodes[0]!.verified = true
      release.stages = stages.map(stage => ({ ...stage, state: 'succeeded', completedAt }))
    } else {
      if (success) task.state.value = 'error'
      release.state = 'failed'
      release.issue = task.state.value === 'cancelled' ? issue('CANCELLED') : success ? issue('CONCURRENT_EDIT') : node.control !== 'online' ? { code: 'NODE_OFFLINE' } : { code: 'DEMO_ERROR' }
      release.nodes[0]!.issue = release.issue
      release.stages = stages.map((stage, i) => ({ ...stage, state: i === 0 ? 'failed' : 'not_checked', ...(i === 0 ? { issue: release.issue, completedAt } : {}) }))
    }
    commands.upsert('releases', release)
    commands.upsert('events', { id: `event-${id}`, name, kind: 'node_config', entityType: 'node', entityId: nodeId, status: release.state, releaseId: id, updatedAt: completedAt })
    return { id, success: !!applied }
  }
  return { ...task, publish }
}
