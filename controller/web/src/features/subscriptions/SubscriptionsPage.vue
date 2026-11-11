<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { useFormat } from '../../composables/useFormat'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NInput, NPagination, NSelect, NTag } from 'naive-ui'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import EntityLink from '../../components/EntityLink.vue'
import QueryStamp from '../../components/QueryStamp.vue'
import { pageLocation, entityLocation } from '../../app/router'
import { useWorkspace } from '../../demo/workspace'
import { effectiveState, usageTotal } from './model'
const { workspace } = useWorkspace(); const { t } = useI18n(); const route = useRoute(); const router = useRouter()
const { date, number } = useFormat()
const tr = (key: string) => t(`features.subscriptions.${key}`)
function query(key: string) { return typeof route.query[key] === 'string' ? route.query[key] : '' }
function change(key: string, value: string | null) { void router.replace(pageLocation('subscriptions', {}, { ...route.query, [key]: value || undefined, page: key === 'page' ? value ?? undefined : undefined })) }
const search = computed({ get: () => query('q'), set: value => change('q', value) })
const status = computed({ get: () => query('status') || null, set: value => change('status', value) })
const sync = computed({ get: () => query('sync') || null, set: value => change('sync', value) })
const routeId = computed({ get: () => query('routeId') || null, set: value => change('routeId', value) })
const sort = computed({ get: () => query('sort') || '-updatedAt', set: value => change('sort', value) })
const invalid = computed(() => (!!status.value && !['active', 'expiring', 'expired', 'revoked'].includes(status.value)) || (!!sync.value && !['synced', 'pending'].includes(sync.value)) || (!!routeId.value && !workspace.routes.some(r => r.id === routeId.value)))
const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  const result = workspace.subscriptions.filter(s => (!q || `${s.name} ${s.id}`.toLowerCase().includes(q)) && (!status.value || effectiveState(s) === status.value) && (!sync.value || s.sync === sync.value) && (!routeId.value || s.routeIds.includes(routeId.value)))
  return [...result].sort((a, b) => { const descending = sort.value.startsWith('-'); const value = sort.value.includes('name') ? a.name.localeCompare(b.name) : a.updatedAt.localeCompare(b.updatedAt); return descending ? -value : value })
})
const pageSize = computed(() => Math.max(1, Math.min(100, Number(query('pageSize')) || 10)))
const page = computed(() => Math.max(1, Math.min(Math.ceil(filtered.value.length / pageSize.value) || 1, Number(query('page')) || 1)))
const rows = computed(() => filtered.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
const hasFilters = computed(() => !!(search.value || status.value || sync.value || routeId.value))
const drafts = computed(() => workspace.drafts.filter(d => d.feature === 'subscriptions'))
function usageLabel(subId: string) {
  const sub = workspace.subscriptions.find(s => s.id === subId)
  const snapshots = workspace.usage.filter(u => u.subscriptionId === subId)
  if (!snapshots.length || snapshots.every(s => s.bytes === null && s.connections == null)) return tr('notQueried')
  const total = usageTotal(snapshots, sub?.entryIds ?? [])
  return total.value === null ? tr('partial') : t(`features.subscriptions.${total.metric === 'connection_count' ? 'connections' : 'bytes'}`, { value: number(total.value) })
}
function lastQuery(subId: string) { return workspace.usage.filter(u => u.subscriptionId === subId).map(u => u.queriedAt).sort().at(-1) }
function clear() { void router.replace(pageLocation('subscriptions')) }
</script>
<template>
  <PageHeader :title="tr('SubscriptionsPage.title')" :description="tr('SubscriptionsPage.goal')"><template #actions><RouterLink :to="pageLocation('subscription-new', {}, { step: 'basics' })"><NButton type="primary">{{ tr('create') }}</NButton></RouterLink></template></PageHeader>
  <nav class="subnav" :aria-label="tr('config')"><RouterLink :to="pageLocation('subscriptions')" aria-current="page">{{ tr('subscriptions') }}</RouterLink><RouterLink :to="pageLocation('rule-profiles')">{{ tr('profiles') }}</RouterLink></nav>
  <NAlert v-if="workspace.scenario === 'error'" type="warning" class="notice">{{ tr('queryFailed') }}</NAlert>
  <section class="panel">
    <div class="filters"><NInput v-model:value="search" :maxlength="120" clearable :placeholder="tr('search')" :aria-label="tr('search')" /><NSelect v-model:value="status" clearable :placeholder="tr('status')" :aria-label="tr('status')" :options="['active', 'expiring', 'expired', 'revoked'].map(value => ({ label: tr(value), value }))" /><NSelect v-model:value="sync" clearable :placeholder="tr('sync')" :aria-label="tr('sync')" :options="['synced', 'pending'].map(value => ({ label: tr(value), value }))" /><NSelect v-model:value="routeId" clearable :placeholder="tr('routeFilter')" :aria-label="tr('routeFilter')" :options="workspace.routes.map(r => ({ label: r.name, value: r.id }))" /><NSelect v-model:value="sort" :aria-label="tr('sort')" :options="[{ label: tr('newest'), value: '-updatedAt' }, { label: tr('oldest'), value: 'updatedAt' }, { label: tr('nameAsc'), value: 'name' }, { label: tr('nameDesc'), value: '-name' }]" /><NButton :disabled="!hasFilters" @click="clear">{{ tr('clear') }}</NButton></div>
    <NAlert v-if="invalid" type="warning" class="notice">{{ tr('unavailableFilter') }}</NAlert>
    <div v-if="rows.length" class="table-scroll"><table><thead><tr><th>{{ tr('name') }}</th><th>{{ tr('expiry') }}</th><th>{{ tr('routes') }}</th><th>{{ tr('profile') }}</th><th>{{ tr('sync') }}</th><th>{{ tr('usage') }}</th><th>{{ tr('view') }}</th></tr></thead><tbody><tr v-for="sub in rows" :key="sub.id"><td><EntityLink kind="subscription" :id="sub.id" :label="sub.name" /><small>{{ sub.description }}</small></td><td><NTag :type="effectiveState(sub) === 'active' ? 'success' : effectiveState(sub) === 'expiring' ? 'warning' : 'default'" size="small">{{ tr(effectiveState(sub)) }}</NTag><small>{{ date(sub.expiresAt) }}</small><small v-if="['active', 'expiring'].includes(effectiveState(sub))">{{ t('features.subscriptions.remaining', { days: Math.max(0, Math.ceil((Date.parse(sub.expiresAt) - Date.now()) / 86400000)) }) }}</small></td><td>{{ sub.routeIds.length }}</td><td><EntityLink kind="profile" :id="sub.profileId" :label="workspace.profiles.find(p => p.id === sub.profileId)?.name ?? sub.profileId" /></td><td><NTag :type="sub.sync === 'pending' ? 'warning' : 'success'" size="small">{{ tr(sub.sync) }}</NTag></td><td>{{ usageLabel(sub.id) }}<QueryStamp :queried-at="lastQuery(sub.id)" :scope="tr('entries')" /></td><td><RouterLink :to="entityLocation('subscription', sub.id)">{{ tr('view') }}</RouterLink></td></tr></tbody></table></div>
    <EmptyState v-else :title="tr(hasFilters ? 'noMatches' : 'noSubscriptions')" :description="tr('emptyHint')" :filtered="hasFilters" :action-label="tr('create')" @clear="clear" @action="router.push(pageLocation('subscription-new'))" />
    <NPagination v-if="filtered.length > pageSize" :page="page" :page-size="pageSize" :item-count="filtered.length" :page-slot="5" class="pagination" @update:page="value => change('page', String(value))" />
  </section>
  <section v-if="drafts.length" class="panel"><h2>{{ tr('savedDrafts') }}</h2><div v-for="draft in drafts" :key="draft.id" class="draft-row"><div><strong>{{ draft.name || tr('draft') }}</strong><small>{{ date(draft.updatedAt) }} · {{ tr(draft.step && ['basics', 'routes', 'policy', 'review'].includes(draft.step) ? draft.step : 'basics') }}</small></div><RouterLink :to="pageLocation(draft.objectId ? 'subscription-edit' : 'subscription-new', draft.objectId ? { subscriptionId: draft.objectId } : {}, { draftId: draft.id, step: draft.step ?? 'basics' })">{{ tr('resume') }}</RouterLink></div></section>
</template>
<style scoped>
.subnav { display: flex; flex-wrap: wrap; gap: 24px; margin: -8px 0 24px; }.subnav a { min-height: 44px; display: flex; align-items: center; }.subnav [aria-current] { font-weight: 600; border-bottom: 2px solid var(--color-primary); }.filters { display: grid; grid-template-columns: 2fr 1fr 1fr; gap: 12px; margin-bottom: 24px; }table { width: 100%; border-collapse: collapse; min-width: 920px; }th, td { text-align: left; padding: 16px 10px; border-bottom: 1px solid var(--color-border); vertical-align: top; }th { color: var(--color-muted); font-weight: 500; }small { display: block; color: var(--color-muted); font-size: 12px; margin-top: 8px; }.pagination { margin-top: 24px; }.draft-row { display: flex; justify-content: space-between; align-items: center; gap: 16px; padding: 16px 0; border-bottom: 1px solid var(--color-border); }.notice { margin-bottom: 20px; }
@media (max-width: 760px) { .filters { grid-template-columns: 1fr; } }
</style>
