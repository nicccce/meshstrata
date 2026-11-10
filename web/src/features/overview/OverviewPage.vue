<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NInput, NSelect, NPagination, NButton } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { pageLocation, entityLocation } from '../../app/router'
import { useWorkspace, resetEpoch } from '../../demo/workspace'
import { useFormat } from '../../composables/useFormat'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import SnapshotPanel from './SnapshotPanel.vue'
import { summarize, onboarding, pendingItems, eventObject, type TodoCategory, type Destination } from './model'

const { t, te } = useI18n()
const { number, date } = useFormat()
const { workspace } = useWorkspace()
const now = ref(Date.now())
const search = ref('')
const category = ref<TodoCategory | 'all'>('all')
const page = ref(1)
const pageSize = 6
watch(() => workspace, () => { now.value = Date.now() }, { deep: true })
watch(resetEpoch, () => { search.value = ''; category.value = 'all'; page.value = 1 })
const counts = computed(() => summarize(workspace, now.value))
const stage = computed(() => onboarding(workspace))
const stageLocation = computed(() => pageLocation(stage.value === 'nodes' ? 'node-new' : stage.value === 'routes' ? 'route-new' : 'subscription-new'))
const stats = computed(() => [
  { id: 'nodes', count: counts.value.nodes, location: pageLocation('nodes') },
  { id: 'routes', count: counts.value.routes, location: pageLocation('routes') },
  // Effective subscriptions include Active and Expiring; no single shared query
  // enum expresses that union, so the card opens the complete subscriptions list.
  { id: 'subscriptions', count: counts.value.subscriptions, location: pageLocation('subscriptions') },
])
const todos = computed(() => pendingItems(workspace, now.value))
const categories: TodoCategory[] = ['rollback', 'failure', 'expiry', 'cleanup', 'offline', 'sync', 'publish']
const categoryOptions = computed(() => [
  { value: 'all', label: t('features.overview.todos.all') },
  ...categories.map(value => ({ value, label: `${t(`features.overview.todos.categories.${value}`)} (${number(todos.value.filter(todo => todo.category === value).length)})` })),
])
const filteredTodos = computed(() => {
  const q = search.value.trim().toLocaleLowerCase()
  return todos.value.filter(todo => (category.value === 'all' || todo.category === category.value) && (!q || `${todo.name} ${todo.objectId} ${todo.taskId ?? ''} ${t(`features.overview.todos.categories.${todo.category}`)}`.toLocaleLowerCase().includes(q)))
})
watch([search, category], () => { page.value = 1 })
watch(() => filteredTodos.value.length, count => { page.value = Math.min(page.value, Math.max(1, Math.ceil(count / pageSize))) })
const visibleTodos = computed(() => filteredTodos.value.slice((page.value - 1) * pageSize, page.value * pageSize))
const recent = computed(() => [...workspace.events].sort((a, b) => (Date.parse(b.updatedAt) || 0) - (Date.parse(a.updatedAt) || 0) || a.id.localeCompare(b.id)).slice(0, 5))
const recentTasks = computed(() => [...workspace.releases].sort((a, b) => (Date.parse(b.updatedAt) || 0) - (Date.parse(a.updatedAt) || 0) || a.id.localeCompare(b.id)).slice(0, 3))
function to(destination: Destination) { return pageLocation(destination.page, destination.params, destination.query) }
function clearFilters() { search.value = ''; category.value = 'all' }
function updateTime() { now.value = Date.now() }
function statusLabel(status: string) {
  const key = `features.overview.states.${status}`
  return te(key) ? t(key) : te(`status.${status}`) ? t(`status.${status}`) : t('common.unknown')
}
</script>

<template>
  <div class="overview-page" @focusin="updateTime">
    <PageHeader :title="t('features.overview.OverviewPage.title')" :description="t('features.overview.OverviewPage.goal')">
      <template #actions><RouterLink class="primary-link" :to="stage ? stageLocation : pageLocation('route-new')">{{ stage ? t(`features.overview.guide.${stage}.action`) : t('features.overview.createRoute') }}<span aria-hidden="true"> ↗</span></RouterLink></template>
    </PageHeader>

    <div class="statistics" :aria-label="t('features.overview.statistics.label')">
      <RouterLink v-for="stat in stats" :key="stat.id" class="stat" :to="stat.location">
        <span class="stat-label">{{ t(`features.overview.statistics.${stat.id}`) }}<span aria-hidden="true"> ↗</span></span>
        <strong>{{ number(stat.count) }}</strong>
        <span class="stat-hint">{{ t(`features.overview.statistics.${stat.id}Hint`) }}</span>
      </RouterLink>
    </div>

    <section v-if="stage" class="panel guide" aria-labelledby="guide-title">
      <div><span class="eyebrow">{{ t('features.overview.guide.stage', { value: stage === 'nodes' ? 1 : stage === 'routes' ? 2 : 3 }) }}</span><h2 id="guide-title">{{ t(`features.overview.guide.${stage}.title`) }}</h2><p>{{ t(`features.overview.guide.${stage}.description`) }}</p></div>
      <RouterLink class="guide-action" :to="stageLocation">{{ t(`features.overview.guide.${stage}.action`) }} →</RouterLink>
    </section>

    <div class="work-grid">
      <section class="panel todo-panel" aria-labelledby="todo-title">
        <div class="section-heading"><h2 id="todo-title">{{ t('features.overview.todos.title') }} <span class="count">{{ number(todos.length) }}</span></h2><RouterLink :to="pageLocation('activity', {}, { tab: 'releases' })">{{ t('features.overview.todos.tasks') }}</RouterLink></div>
        <p class="section-hint">{{ t('features.overview.todos.hint') }}</p>
        <div class="filters"><div><label for="overview-todo-search">{{ t('features.overview.todos.search') }}</label><NInput :input-props="{ id: 'overview-todo-search' }" v-model:value="search" clearable :maxlength="120" :placeholder="t('features.overview.todos.searchHint')" /></div><div><label for="overview-todo-category">{{ t('features.overview.todos.category') }}</label><NSelect :input-props="{ id: 'overview-todo-category' }" v-model:value="category" :options="categoryOptions" filterable /></div></div>
        <div class="list-summary"><span>{{ t('features.overview.todos.results', { value: number(filteredTodos.length) }) }}</span><NButton v-if="search || category !== 'all'" text @click="clearFilters">{{ t('common.clear') }}</NButton></div>
        <EmptyState v-if="!filteredTodos.length" :filtered="!!search || category !== 'all'" :title="t(todos.length ? 'features.overview.todos.noMatches' : 'features.overview.todos.clear')" :description="t(todos.length ? 'features.overview.todos.noMatchesHint' : 'features.overview.todos.clearHint')" @clear="clearFilters" />
        <ul v-else class="todo-list">
          <li v-for="todo in visibleTodos" :key="todo.id">
            <div class="todo-top"><span class="category" :class="todo.category">{{ t(`features.overview.todos.categories.${todo.category}`) }}</span><span v-if="todo.detail" class="todo-detail">{{ todo.detail === 'revoke' || todo.detail === 'expiring' ? t(`features.overview.todos.${todo.detail}`) : statusLabel(todo.detail) }}</span></div>
            <RouterLink class="object-link" :to="to(todo.destination)">{{ todo.name }} <span aria-hidden="true">→</span></RouterLink>
            <div class="todo-meta"><code>{{ todo.objectId }}</code><span>{{ date(todo.updatedAt) }}</span><RouterLink v-if="todo.taskId" :to="entityLocation('release', todo.taskId)">{{ t('features.overview.todos.openTask') }}</RouterLink></div>
          </li>
        </ul>
        <NPagination v-if="filteredTodos.length > pageSize" v-model:page="page" :page-size="pageSize" :item-count="filteredTodos.length" :page-slot="5" :aria-label="t('features.overview.todos.pagination')" />
      </section>

      <section class="panel recent-panel" aria-labelledby="recent-title">
        <div class="section-heading"><h2 id="recent-title">{{ t('features.overview.recent.title') }}</h2><RouterLink :to="pageLocation('activity', {}, { tab: 'events' })">{{ t('features.overview.recent.all') }}</RouterLink></div>
        <p v-if="!recent.length" class="section-hint">{{ t('features.overview.recent.empty') }}</p>
        <ol v-else class="event-list"><li v-for="event in recent" :key="event.id">
          <div class="event-top"><time :datetime="event.updatedAt">{{ date(event.updatedAt) }}</time><StatusBadge :status="event.status" :label="statusLabel(event.status)" /></div>
          <strong>{{ event.name }}</strong>
          <RouterLink :to="entityLocation(event.entityType, event.entityId)">{{ eventObject(workspace, event)?.name ?? event.entityId }}</RouterLink>
          <span v-if="!eventObject(workspace, event)" class="muted">{{ t('features.overview.recent.missing') }}</span>
          <div class="event-actions"><RouterLink :to="pageLocation('activity', {}, { tab: 'events', entityType: event.entityType, entityId: event.entityId })">{{ t('features.overview.recent.context') }}</RouterLink><RouterLink v-if="event.releaseId" :to="entityLocation('release', event.releaseId)">{{ t('features.overview.todos.openTask') }}</RouterLink></div>
        </li></ol>
        <h3 v-if="recentTasks.length">{{ t('features.overview.recent.tasks') }}</h3>
        <ul v-if="recentTasks.length" class="task-list"><li v-for="release in recentTasks" :key="release.id"><RouterLink :to="entityLocation('release', release.id)">{{ release.name }}</RouterLink><span class="muted">{{ statusLabel(release.state) }} · {{ date(release.updatedAt) }}</span><RouterLink v-if="release.routeId" :to="entityLocation('route', release.routeId)">{{ workspace.routes.find(route => route.id === release.routeId)?.name ?? release.routeId }}</RouterLink><RouterLink v-else-if="release.subscriptionId" :to="entityLocation('subscription', release.subscriptionId)">{{ workspace.subscriptions.find(subscription => subscription.id === release.subscriptionId)?.name ?? release.subscriptionId }}</RouterLink><RouterLink v-else-if="release.nodeId" :to="entityLocation('node', release.nodeId)">{{ workspace.nodes.find(node => node.id === release.nodeId)?.name ?? release.nodeId }}</RouterLink></li></ul>
      </section>
    </div>

    <SnapshotPanel />
    <div class="overview-footer"><p>{{ t('features.overview.footer') }}</p><RouterLink :to="pageLocation('settings', {}, { section: 'general' })">{{ t('features.overview.controllerInfo') }} →</RouterLink></div>
  </div>
</template>

<style scoped>
.primary-link { display: inline-flex; align-items: center; min-height: 44px; padding: 10px 18px; border-radius: 8px; color: white; background: var(--color-primary); text-decoration: none; }
.statistics { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 20px; margin-bottom: 24px; }
.stat { display: flex; flex-direction: column; gap: 12px; padding: 24px; border: 1px solid var(--color-border); border-radius: var(--radius); background: var(--color-surface); color: var(--color-text); text-decoration: none; }
.stat:hover { border-color: var(--color-primary); background: #f4faf6; }
.stat-label { display: flex; justify-content: space-between; font-size: 14px; }
.stat-label > span { color: var(--color-primary); }
.stat strong { font-size: 36px; line-height: 1.1; font-weight: 600; }
.stat-hint { color: var(--color-muted); font-size: 12px; line-height: 1.6; }
.guide { display: flex; align-items: center; justify-content: space-between; gap: 24px; background: #edf5ef; }
.guide h2 { margin: 8px 0; }.guide p { margin: 0; }
.guide-action { flex-shrink: 0; min-height: 44px; display: inline-flex; align-items: center; font-weight: 600; }
.work-grid { display: grid; grid-template-columns: minmax(0, 1.35fr) minmax(0, 1fr); gap: 24px; align-items: start; }
.section-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.section-heading h2 { margin: 0; }.section-heading > a { display: inline-flex; align-items: center; min-height: 44px; font-size: 12px; }
.count { font-size: 12px; background: #edf5ef; border-radius: 12px; padding: 4px 8px; margin-left: 6px; color: var(--color-primary); }
.section-hint { margin: 4px 0 20px; font-size: 12px; }
.filters { display: grid; grid-template-columns: 1.1fr 1fr; gap: 12px; }.filters > div { min-width: 0; }.filters label { font-size: 12px; }
.filters :deep(.n-base-selection-label) { min-height: 44px; }.filters :deep(.n-input__input-el) { height: 44px; }
.list-summary { display: flex; justify-content: space-between; align-items: center; min-height: 44px; font-size: 12px; color: var(--color-muted); }
.todo-list, .event-list, .task-list { list-style: none; padding: 0; margin: 0; }
.todo-list li { padding: 16px 0; border-top: 1px solid var(--color-border); }
.todo-top { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.category { color: var(--color-primary); background: #edf5ef; border-radius: 5px; padding: 3px 8px; font-size: 11px; }
.category.rollback, .category.failure, .category.expiry { color: #8a4a24; background: #fcf1e8; }
.todo-detail { font-size: 12px; color: var(--color-muted); }
.object-link { display: flex; justify-content: space-between; align-items: center; gap: 12px; min-height: 44px; font-weight: 600; text-decoration: none; }
.todo-meta { display: flex; gap: 6px 14px; flex-wrap: wrap; font-size: 11px; color: var(--color-muted); }.todo-meta > a { min-height: 44px; display: inline-flex; align-items: center; margin-top: -14px; margin-bottom: -14px; }
.event-list li { display: grid; gap: 10px; border-top: 1px solid var(--color-border); padding: 20px 0; }
.event-top { display: flex; justify-content: space-between; gap: 8px; flex-wrap: wrap; align-items: center; }.event-top time { font-size: 11px; color: var(--color-muted); }
.event-list li > a { display: inline-flex; align-items: center; min-height: 44px; margin: -8px 0; }
.event-actions { display: flex; gap: 12px; flex-wrap: wrap; font-size: 12px; }.event-actions > a { min-height: 44px; display: inline-flex; align-items: center; }
.recent-panel h3 { font-size: 14px; margin: 12px 0; }.task-list li { display: grid; gap: 6px; padding: 12px 0; border-top: 1px solid var(--color-border); }.task-list a { display: inline-flex; align-items: center; min-height: 44px; }
.muted { color: var(--color-muted); font-size: 12px; }
.overview-footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; color: var(--color-muted); font-size: 12px; }.overview-footer a { min-height: 44px; display: inline-flex; align-items: center; }
.todo-panel :deep(.n-pagination) { margin-top: 16px; }.todo-panel :deep(.state-panel) { margin: 0; padding: 20px 0; border: 0; }
@media (max-width: 1100px) { .work-grid { grid-template-columns: minmax(0, 1fr); } }
@media (max-width: 760px) { .statistics { grid-template-columns: 1fr; gap: 12px; }.stat { padding: 20px; }.stat strong { font-size: 30px; }.guide { align-items: start; flex-direction: column; gap: 12px; }.filters { grid-template-columns: 1fr; }.todo-meta { font-size: 12px; }.todo-meta > a { margin: 0; }.todo-panel :deep(.n-pagination-item) { min-height: 36px; min-width: 36px; } }
</style>
