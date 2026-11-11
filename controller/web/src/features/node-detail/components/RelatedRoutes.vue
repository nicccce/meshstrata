<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NInput, NSelect, NButton } from 'naive-ui'
import { pageLocation, entityLocation } from '../../../app/router'
import { useFormat } from '../../../composables/useFormat'
import StatusBadge from '../../../components/StatusBadge.vue'
import { useNodeContext } from '../useNodeContext'
import { routeRoles } from '../model'
const props = defineProps<{ nodeId: string }>()
const { node, routes, workspace } = useNodeContext(props.nodeId)
const { t } = useI18n(); const { date } = useFormat()
const key = 'features.node-detail.'
const search = ref(''); const role = ref('all'); const state = ref('all')
const roles = computed(() => ['all', 'entry', 'transit', 'exit'].map(value => ({ value, label: t(key + 'roles.' + value) })))
const states = computed(() => [{ value: 'all', label: t(key + 'allStates') }, ...[...new Set(routes.value.map(r => r.state))].map(value => ({ value, label: t(key + 'releaseStates.' + value) }))])
const filtered = computed(() => routes.value.filter(r => (!search.value || `${r.name} ${r.id}`.toLowerCase().includes(search.value.trim().toLowerCase())) && (role.value === 'all' || routeRoles(r, props.nodeId).includes(role.value)) && (state.value === 'all' || state.value === r.state)))
function pathNames(ids: readonly string[]) { return ids.map(id => workspace.nodes.find(n => n.id === id)?.name ?? id).join(' → ') }
function clear() { search.value = ''; role.value = 'all'; state.value = 'all' }
</script>
<template>
  <section class="panel">
    <div class="heading"><div><h2>{{ t(key + 'tabs.routes') }}</h2><p>{{ t(key + 'routesHint') }}</p></div><RouterLink v-if="!node.removedAt" class="create-link" :to="pageLocation('route-new', {}, { exitNodeId: nodeId, step: 'exit' })">{{ t(key + 'createExit') }}</RouterLink></div>
    <div class="filters"><NInput v-model:value="search" clearable :placeholder="t(key + 'searchRoutes')" :input-props="{ 'aria-label': t(key + 'searchRoutes') }" /><NSelect v-model:value="role" :options="roles" :aria-label="t(key + 'role')" /><NSelect v-model:value="state" :options="states" :aria-label="t(key + 'publishState')" /></div>
    <p>{{ t(key + 'routeCount', { count: filtered.length, total: routes.length }) }}</p>
    <div v-if="filtered.length" class="table-scroll"><table><caption class="sr-only">{{ t(key + 'tabs.routes') }}</caption><thead><tr><th>{{ t(key + 'name') }}</th><th>{{ t(key + 'role') }}</th><th>{{ t(key + 'path') }}</th><th>{{ t(key + 'publishState') }}</th><th>{{ t(key + 'verification') }}</th><th>{{ t(key + 'updatedAt') }}</th></tr></thead><tbody>
      <tr v-for="r in filtered" :key="r.id"><td><RouterLink :to="entityLocation('route', r.id)">{{ r.name }}</RouterLink><small>{{ r.id }}</small></td><td>{{ routeRoles(r, nodeId).map(value => t(key + 'roles.' + value)).join(' / ') || t('common.unknown') }}</td><td>{{ pathNames((r.published ?? r.draft).nodeIds) }}<small>{{ r.published ? t(key + 'publishedRevision', { revision: r.published.revision }) : t(key + 'draftOnly') }}</small></td><td><StatusBadge :status="r.state" :label="t(key + 'releaseStates.' + r.state)" /><small v-if="r.cleanup === 'pending'">{{ t(key + 'pendingCleanup') }}</small></td><td><StatusBadge :status="r.verification" /></td><td>{{ date(r.updatedAt) }}</td></tr>
    </tbody></table></div>
    <div v-else role="status"><p>{{ t(key + (routes.length ? 'noMatchingRoutes' : 'noRoutes')) }}</p><NButton v-if="routes.length" @click="clear">{{ t('common.clear') }}</NButton></div>
    <div class="actions"><RouterLink :to="pageLocation('routes', {}, { nodeId })">{{ t(key + 'allRelatedRoutes') }}</RouterLink></div>
  </section>
</template>
<style scoped>
.heading { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 16px; }.create-link { min-height: 44px; display: inline-flex; align-items: center; }.filters { display: grid; grid-template-columns: minmax(160px, 1fr) 160px 180px; gap: 12px; }table { border-collapse: collapse; width: 100%; min-width: 720px; }th, td { text-align: left; padding: 16px 12px; border-bottom: 1px solid var(--color-border); }small { display: block; color: var(--color-muted); margin-top: 8px; }.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }@media(max-width:760px){.filters{grid-template-columns:1fr}}
</style>
