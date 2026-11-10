import { computed, reactive, readonly, watch, ref } from 'vue'
import type { Workspace, Scenario, EntityCollection, CollectionEntity, Draft } from '../domain'
import { createSeed } from './seeds'
import { projectDraftData } from './draft-data'
export const storageKey = 'meshstrata.demo.v1'
export const persistenceIssue = ref(false)
const scenarios: Scenario[] = ['normal', 'empty', 'error', 'stale', 'offline', 'partial']
// Never persist credential fields, including accidentally added nested fields.
export const resetEpoch = ref(0)
const sensitive = /secret|password|token|credential|authorization|private.?key|auth/i
export function safeJson(value: unknown): string { return JSON.stringify(value, (key, item) => sensitive.test(key) ? undefined : item) }
export function decodeWorkspace(raw: string): Workspace {
  const v = JSON.parse(raw) as Workspace
  if (!v || v.schemaVersion !== 1 || !scenarios.includes(v.scenario)) throw Error('DEMO_STORAGE_INVALID')
  const seed = createSeed()
  for (const key of Object.keys(seed) as (keyof Workspace)[]) {
    if (Array.isArray(seed[key])) {
      if (!Array.isArray(v[key])) throw Error('DEMO_STORAGE_INVALID')
      const ids = new Set<string>()
      for (const entry of v[key] as { id: string }[]) {
        if (!entry || typeof entry.id !== 'string' || ids.has(entry.id)) throw Error('DEMO_STORAGE_INVALID')
        ids.add(entry.id)
      }
    }
  }
  if (!v.settings || typeof v.settings.relayNodeId !== 'string') throw Error('DEMO_STORAGE_INVALID')
  // Validate runtime field shapes against the seed to reject corrupted snapshots.
  const optionalPaths = new Set([
    'nodes[].address', 'routes[].published', 'routes[].draft.policyId', 'routes[].draft.policyRevision',
    'routes[].published.policyId', 'routes[].published.policyRevision',
    'routes[].draft.links[].tunnelId', 'routes[].draft.links[].tunnelRevision',
    'routes[].published.links[].tunnelId', 'routes[].published.links[].tunnelRevision',
    'targets[].host', 'targets[].port', 'targets[].interfaceName', 'releases[].routeId',
    'releases[].subscriptionId', 'releases[].nodes[].issue',
  ])
  function shape(sample: unknown, value: unknown, path = ''): boolean {
    if (sample === null || sample === undefined) return true
    if (Array.isArray(sample)) return Array.isArray(value) && (!sample.length || value.every(item => shape(sample[0], item, `${path}[]`)))
    if (typeof sample === 'object') return !!value && typeof value === 'object' && Object.entries(sample).every(([key, item]) => {
      const childPath = path ? `${path}.${key}` : key
      const child = (value as Record<string, unknown>)[key]
      return (child === undefined && optionalPaths.has(childPath)) || shape(item, child, childPath)
    })
    return typeof sample === typeof value
  }
  if (!shape(seed, v)) throw Error('DEMO_STORAGE_INVALID')
  for (const draft of v.drafts) {
    if (typeof draft.feature !== 'string' || typeof draft.name !== 'string' || typeof draft.updatedAt !== 'string' || !Array.isArray(draft.nodeIds) || !draft.nodeIds.every(id => typeof id === 'string') || !Array.isArray(draft.routeIds) || !draft.routeIds.every(id => typeof id === 'string')) throw Error('DEMO_STORAGE_INVALID')
    draft.data = projectDraftData(draft.data)
  }
  return JSON.parse(safeJson(v)) as Workspace
}
function restore(): Workspace {
  try { const raw = localStorage.getItem(storageKey); return raw ? decodeWorkspace(raw) : createSeed() }
  catch { persistenceIssue.value = true; return createSeed() }
}
const state = reactive<Workspace>(restore())
watch(state, () => {
  if (persistenceIssue.value) return
  try { localStorage.setItem(storageKey, safeJson(state)) } catch { persistenceIssue.value = true }
}, { deep: true, flush: 'post' })
export const workspace = readonly(state)
export const commands = {
  reset(scenario: Scenario = 'normal') { persistenceIssue.value = false; Object.assign(state, createSeed(scenario)); sessionSecrets.clear(); resetEpoch.value++ },
  upsert<K extends EntityCollection>(collection: K, entity: CollectionEntity<K>) {
    const items = state[collection] as CollectionEntity<K>[]
    const index = items.findIndex(item => item.id === entity.id)
    const clean = JSON.parse(safeJson(entity)) as CollectionEntity<K>
    if (index < 0) items.push(clean); else items[index] = clean
  },
  remove(collection: EntityCollection, id: string) { const items = state[collection]; const i = items.findIndex(item => item.id === id); if (i >= 0) items.splice(i, 1) },
  saveDraft(draft: Draft) {
    // Explicit projection: free-form feature payloads and sensitive inputs are excluded.
    const { id, feature, objectId, name, nodeIds, routeIds, profileId, step, data } = draft
    this.upsert('drafts', { id, feature, objectId, name, nodeIds: [...nodeIds], routeIds: [...routeIds], profileId, step, data: projectDraftData(data), updatedAt: new Date().toISOString() })
  },
  updateSettings(patch: Partial<Workspace['settings']>) { Object.assign(state.settings, patch) },
}
// Separate memory-only storage; never passed to persistence or router.
export const sessionSecrets = reactive(new Map<string, string>())
export const localClientPaired = computed(() => !!state.settings.localClient?.paired && sessionSecrets.has('settings:paired-secret'))
export function useWorkspace() { return { workspace, commands, persistenceIssue, sessionSecrets, localClientPaired } }
