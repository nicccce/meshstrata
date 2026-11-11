import type { LocationQuery, LocationQueryRaw, Router } from 'vue-router'
const enums: Record<string, Record<string, string[]>> = {
  'node-new': { step: ['basics', 'install', 'verify'] }, 'node-detail': { tab: ['overview', 'protocols', 'dns', 'connectivity', 'routes'] },
  'route-new': { step: ['exit', 'paths', 'options', 'review'] }, 'route-edit': { step: ['exit', 'paths', 'options', 'review'] },
  'route-detail': { tab: ['overview', 'measurements', 'resources', 'history'] },
  'subscription-new': { step: ['basics', 'routes', 'policy', 'review'] }, 'subscription-edit': { step: ['basics', 'routes', 'policy', 'review'] },
  'subscription-detail': { tab: ['overview', 'entries', 'config', 'usage'] },
  activity: { tab: ['events', 'releases', 'logs'] }, settings: { section: ['general', 'ports', 'relay', 'location', 'certificates', 'client', 'logs', 'admin'] },
}
const refs: Record<string, string[]> = {
 nodes: ['mode', 'location', 'status', 'environment'], 'node-new': ['draftId'], routes: ['nodeId', 'exitNodeId', 'protocol', 'status', 'udp'], 'route-new': ['exitNodeId', 'draftId'], 'route-edit': ['draftId'],
 subscriptions: ['status', 'sync', 'routeId'], 'subscription-new': ['routeIds', 'profileId', 'draftId', 'returnTo'], 'subscription-edit': ['profileId', 'draftId', 'returnTo'],
 'profile-new': ['draftId', 'returnTo', 'subscriptionId'], 'profile-edit': ['draftId', 'returnTo', 'subscriptionId'], activity: ['kind', 'entityType', 'entityId', 'status'], login: ['returnTo'],
}
const lists = ['nodes', 'routes', 'subscriptions', 'rule-profiles', 'activity']
export function sanitizeQuery(name: string, query: LocationQuery | LocationQueryRaw): LocationQueryRaw {
 const out: LocationQueryRaw = {}
 for (const [key, raw] of Object.entries(query)) {
  if (typeof raw !== 'string' || raw.length > 1200) continue
  const values = Object.hasOwn(enums[name] ?? {}, key) ? enums[name]![key] : undefined
  if (values?.includes(raw)) out[key] = raw
  else if (['route-detail', 'route-edit', 'route-new', 'subscription-detail'].includes(name) && key === 'panel' && ['egress', 'check-egress', 'preview'].includes(raw)) out[key] = raw
  else if (lists.includes(name) && ['page', 'pageSize'].includes(key) && /^[1-9][0-9]{0,3}$/.test(raw)) out[key] = raw
  else if (lists.includes(name) && key === 'q' && raw.length <= 120) out[key] = raw
  else if (lists.includes(name) && key === 'sort' && ['name', '-name', 'updatedAt', '-updatedAt'].includes(raw)) out[key] = raw
  else if (refs[name]?.includes(key) && key !== 'returnTo' && /^[a-zA-Z0-9_., -]{1,160}$/.test(raw)) out[key] = raw
 }
 return out
}

export function createSafeReturnTo(resolve: Router['resolve']) {
 return (value: unknown): string | undefined => {
 if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') || /[\\#\x00-\x20]/.test(value) || value.length > 1200) return
 const resolved = resolve(value)
 if (!resolved.name || ['not-found', 'login'].includes(String(resolved.name))) return
 return resolve({ name: resolved.name, params: resolved.params, query: sanitizeQuery(String(resolved.name), resolved.query) }).fullPath
 }
}
