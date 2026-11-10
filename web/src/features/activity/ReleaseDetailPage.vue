<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton } from 'naive-ui'
import type { Issue, Release } from '../../domain'
import { issueMessageKey } from '../../domain'
import { pageLocation, entityLocation } from '../../app/router'
import { resetEpoch, useWorkspace } from '../../demo/workspace'
import { useFormat } from '../../composables/useFormat'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
import PageHeader from '../../components/PageHeader.vue'
import ConfirmImpactDialog from '../../components/ConfirmImpactDialog.vue'
import ActivitySourceLink from './ActivitySourceLink.vue'
import { canCancel, canRetry, cleanupBlockers, dependencyPlan, releaseNodeIds, releaseSources, rollbackMode, simulateReleaseAttempt, type ReleaseAction } from './activity-model'

const { t, te } = useI18n()
const f = (key: string, params: Record<string, string | number> = {}) => t(`features.activity.${key}`, params)
const label = (group: string, value: string) => te(`features.activity.${group}.${value}`) ? f(`${group}.${value}`) : value
function issueText(issue: Issue) {
  const key = issueMessageKey(issue.code)
  return te(key) ? t(key, issue.params ?? {}) : issue.code
}
const route = useRoute()
const router = useRouter()
const { workspace, commands } = useWorkspace()
const { date } = useFormat()
const release = computed(() => workspace.releases.find(item => item.id === String(route.params.releaseId)))
const task = useSimulatedTask()
const busy = computed(() => task.state.value === 'running')
const feedback = ref('')
const feedbackError = ref(false)
const action = ref<ReleaseAction>()
const dialog = ref(false)
const sources = computed(() => release.value ? releaseSources(release.value) : [])
const nodeIds = computed(() => release.value ? releaseNodeIds(release.value, workspace) : [])
const nodeRows = computed(() => nodeIds.value.map(id => ({ id, node: workspace.nodes.find(node => node.id === id), result: release.value?.nodes.find(node => node.nodeId === id) })))
const offline = computed(() => nodeRows.value.some(row => !row.node || row.node.control !== 'online' || row.node.removedAt))
const incomplete = computed(() => !!release.value && (!nodeRows.value.length || nodeRows.value.some(row => !row.result?.applied || !row.result.verified) || !!release.value.stages?.some(stage => stage.state !== 'succeeded')))
const state = computed(() => release.value?.issue?.code === 'features.activity.issues.CANCELLED' ? 'cancelled' : release.value?.state ?? 'unknown')
const plan = computed(() => release.value ? dependencyPlan(release.value, workspace) : [])
const blockers = computed(() => release.value ? cleanupBlockers(release.value, workspace) : [])
const sourceRoute = computed(() => workspace.routes.find(item => item.id === release.value?.routeId))
const isRollback = computed(() => !!release.value && rollbackMode(release.value))
const retryAllowed = computed(() => !!release.value && canRetry(release.value) && state.value !== 'cancelled')
const rollbackAllowed = computed(() => release.value?.kind === 'publish' && release.value.nodes.some(node => node.applied) && !isRollback.value && state.value !== 'cancelled')
const cancelAllowed = computed(() => !!release.value && canCancel(release.value))
const actionLabel = computed(() => action.value ? f(action.value) : '')
const impacts = computed(() => [
  f(action.value === 'rollback' ? 'confirmRollback' : 'confirmImpact', { id: release.value?.id ?? '' }),
  f('confirmSimulation'), f('confirmPreserve'),
  ...(action.value === 'cleanup' ? [f('cleanupHint')] : []),
])
watch(() => route.params.releaseId, () => { task.cancel(); dialog.value = false; feedback.value = ''; action.value = undefined })
watch(resetEpoch, () => { task.cancel(); dialog.value = false; feedback.value = ''; action.value = undefined })
function requestAction(next: ReleaseAction) {
  if (busy.value || !release.value) return
  if (next === 'retry' && !retryAllowed.value || next === 'cancel' && !cancelAllowed.value || next === 'rollback' && !rollbackAllowed.value || next === 'cleanup' && release.value.cleanup !== 'pending') return
  action.value = next
  feedback.value = ''
  dialog.value = true
}
async function runAction() {
  const original = release.value
  const requested = action.value
  if (!original || !requested || busy.value) return
  const epoch = resetEpoch.value
  const snapshot = JSON.stringify(original)
  // Capture relevant dependencies too: do not overwrite a task after a sibling
  // changes its nodes, route revision, subscription or resource references.
  const dependencies = JSON.stringify([workspace.nodes, workspace.routes, workspace.subscriptions, workspace.entries, workspace.listeners, workspace.tunnels, workspace.scenario])
  const finished = await task.run('success', 650)
  if (!finished) return
  dialog.value = false
  if (epoch !== resetEpoch.value || release.value?.id !== original.id || JSON.stringify(release.value) !== snapshot || dependencies !== JSON.stringify([workspace.nodes, workspace.routes, workspace.subscriptions, workspace.entries, workspace.listeners, workspace.tunnels, workspace.scenario])) {
    feedback.value = f('changed'); feedbackError.value = true; return
  }
  const now = new Date().toISOString()
  let base: Release = JSON.parse(snapshot)
  if (requested === 'rollback') {
    base = { ...base, id: `release-rollback-${crypto.randomUUID()}`, name: f('rollbackTask', { name: original.name }), kind: 'rollback', state: 'rolling_back', updatedAt: now, startedAt: now, completedAt: undefined, stages: undefined, issue: undefined,
      nodes: base.nodes.map(node => ({ ...node, rollback: 'pending', issue: undefined })) }
  }
  const result = simulateReleaseAttempt(base, workspace, requested, now)
  commands.upsert('releases', result)
  commands.upsert('events', { id: `event-release-${crypto.randomUUID()}`, name: f('taskEvent', { action: label('kinds', requested === 'retry' ? original.kind : requested), name: original.name }), updatedAt: now,
    kind: requested === 'retry' ? original.kind : requested, entityType: 'release', entityId: result.id, releaseId: result.id, status: requested === 'cancel' ? 'cancelled' : result.state })
  feedback.value = f(requested === 'cancel' ? 'cancelledFeedback' : 'completedFeedback')
  feedbackError.value = result.state.includes('failed') || result.state.includes('pending')
  if (requested === 'rollback') await router.push(entityLocation('release', result.id))
}
</script>

<template>
  <template v-if="!release">
    <PageHeader :title="f('notFound')" :description="f('notFoundHint')" />
    <RouterLink :to="pageLocation('activity', {}, { tab: 'releases' })">{{ f('relatedTasks') }}</RouterLink>
  </template>
  <template v-else>
    <PageHeader :title="release.name" :description="f('ReleaseDetailPage.goal')">
      <template #actions><div class="header-links"><RouterLink :to="pageLocation('activity', {}, { tab: 'releases' })">{{ f('relatedTasks') }}</RouterLink><RouterLink :to="pageLocation('activity', {}, { tab: 'events', entityType: 'release', entityId: release.id })">{{ f('relatedEvents') }}</RouterLink></div></template>
    </PageHeader>
    <NAlert type="info" class="notice">{{ f('unverifiedWarning') }}</NAlert>
    <NAlert v-if="release.state === 'published' && incomplete" type="warning" class="notice">{{ f('mismatchWarning') }}</NAlert>
    <NAlert v-if="offline" type="warning" class="notice"><strong>{{ f('states.node_sync_pending') }}</strong><p>{{ f('offlineHint') }}</p></NAlert>
    <NAlert v-if="release.issue" type="error" class="notice">{{ issueText(release.issue) }}<p v-if="release.issue.field">{{ f('field') }}: {{ release.issue.field }}</p></NAlert>
    <NAlert v-if="feedback" :type="feedbackError ? 'warning' : 'success'" class="notice" role="status">{{ feedback }}</NAlert>
    <section class="panel">
      <h2>{{ f('summary') }}</h2>
      <dl class="summary-grid"><div><dt>{{ f('types.release') }}</dt><dd>{{ release.id }}</dd></div><div><dt>{{ f('columns.action') }}</dt><dd>{{ label('kinds', release.kind) }}</dd></div><div><dt>{{ f('storedState') }}</dt><dd><span class="result" :class="{ warning: incomplete || release.state.includes('failed') || release.state.includes('pending') }">{{ label('states', release.state === 'published' && incomplete ? 'applied_unverified' : state) }}</span></dd></div><div><dt>{{ f('columns.revision') }}</dt><dd>{{ release.revision }}</dd></div>
        <div><dt>{{ f('started') }}</dt><dd>{{ release.startedAt ? date(release.startedAt) : f('notRecorded') }}</dd></div><div><dt>{{ f('completed') }}</dt><dd>{{ release.completedAt ? date(release.completedAt) : f('notComplete') }}</dd></div><div><dt>{{ f('columns.updated') }}</dt><dd>{{ date(release.updatedAt) }}</dd></div><div><dt>{{ f('columns.cleanup') }}</dt><dd>{{ label('states', release.cleanup === 'pending' ? 'cleanup_pending' : release.cleanup) }}</dd></div>
        <div v-if="release.routeId"><dt>{{ f('publishedRevision') }}</dt><dd>{{ sourceRoute?.published?.revision ?? f('notRecorded') }}</dd></div><div v-if="release.sourceDraftId"><dt>{{ f('sourceDraft') }}</dt><dd>{{ release.sourceDraftId }}</dd></div>
      </dl>
      <h3>{{ f('sources') }}</h3><div class="source-links"><div v-for="source in sources" :key="`${source.kind}-${source.id}`"><span>{{ f(`types.${source.kind}`) }} · </span><ActivitySourceLink :source="source" /></div></div>
      <p>{{ f('preserved') }}</p>
    </section>
    <section class="panel"><h2>{{ f('nodeResults') }}</h2><p>{{ f('nodeHint') }}</p>
      <div v-if="nodeRows.length" class="table-scroll"><table><thead><tr><th>{{ f('columns.node') }}</th><th>{{ f('columns.control') }}</th><th>{{ f('columns.applied') }}</th><th>{{ f('columns.verified') }}</th><th>{{ f('columns.rollback') }}</th><th>{{ f('columns.issue') }}</th></tr></thead><tbody>
        <tr v-for="row in nodeRows" :key="row.id"><td><ActivitySourceLink :source="{ kind: 'node', id: row.id }" /><small>{{ row.id }}</small><RouterLink class="log-link" :to="pageLocation('activity', {}, { tab: 'logs', entityType: 'node', entityId: row.id })">{{ f('tabs.logs') }}</RouterLink></td><td>{{ label('states', row.node?.control ?? 'unknown') }}</td><td>{{ row.result?.applied ? f('yes') : f('no') }}</td><td>{{ row.result?.applied && row.result.verified && row.result.rollback === 'none' ? f('yes') : f('no') }}<small v-if="row.node?.control !== 'online' && row.result?.verified">{{ f('historical') }}</small></td><td>{{ label('states', row.result?.rollback ?? 'none') }}<small v-if="row.node?.control !== 'online' && row.result?.rollback === 'confirmed'">{{ f('historical') }}</small></td><td><template v-if="row.result?.issue">{{ issueText(row.result.issue) }}<small v-if="row.result.issue.field">{{ f('field') }}: {{ row.result.issue.field }}</small></template><template v-else-if="row.node?.control !== 'online'">{{ f('offlineHint') }}</template><template v-else>{{ f('states.not_checked') }}</template></td></tr>
      </tbody></table></div><NAlert v-else type="warning">{{ f('noNodes') }}</NAlert>
    </section>
    <div class="detail-columns">
      <section class="panel"><h2>{{ f('stageTitle') }}</h2><p>{{ release.stages?.length ? f('recordedStageHint') : f('noStages') }}</p><ol v-if="release.stages?.length" class="stage-list"><li v-for="stage in release.stages" :key="stage.id"><div class="stage-heading"><strong>{{ stage.name }}</strong><span class="result" :class="{ warning: stage.state === 'failed' }">{{ label('states', stage.state) }}</span></div><p v-if="stage.nodeId"><ActivitySourceLink :source="{ kind: 'node', id: stage.nodeId }" /></p><p v-if="stage.completedAt">{{ date(stage.completedAt) }}</p><div v-if="stage.issue" role="status">{{ issueText(stage.issue) }}<p v-if="stage.issue.field">{{ f('field') }}: {{ stage.issue.field }}</p></div></li></ol></section>
      <section class="panel"><h2>{{ f('dependencyTitle') }}</h2><p>{{ f('dependencyHint') }}</p><ol class="dependency-list"><li v-for="step in plan" :key="step.id">{{ f(`steps.${step.key}`) }}<div v-if="step.nodeId"><ActivitySourceLink :source="{ kind: 'node', id: step.nodeId }" /></div></li></ol><template v-if="isRollback || release.kind !== 'publish'"><h3>{{ f('operationsOrder') }}</h3><p>{{ f(isRollback ? 'rollbackOrder' : release.kind === 'revoke' ? 'revokeOrder' : 'cleanupOrder') }}</p></template></section>
    </div>
    <section class="panel"><h2>{{ f('next') }}</h2><p>{{ f('retryHint') }}</p><p v-if="isRollback || rollbackAllowed">{{ f('rollbackHint') }}</p>
      <NAlert v-if="release.cleanup === 'pending'" type="warning"><strong>{{ f('states.cleanup_pending') }}</strong><p>{{ f('cleanupHint') }}</p><p v-if="blockers.length">{{ f('cleanupBlocked', { count: blockers.length }) }}</p><details v-if="blockers.length"><summary>{{ f('resources') }}</summary><ul><li v-for="id in blockers" :key="id"><code>{{ id }}</code></li></ul></details></NAlert>
      <div class="actions"><NButton v-if="retryAllowed" type="primary" :disabled="busy" @click="requestAction('retry')">{{ f('retry') }}</NButton><NButton v-if="rollbackAllowed" :disabled="busy" @click="requestAction('rollback')">{{ f('rollback') }}</NButton><NButton v-if="release.cleanup === 'pending'" :disabled="busy" @click="requestAction('cleanup')">{{ f('cleanup') }}</NButton><NButton v-if="cancelAllowed" :disabled="busy" @click="requestAction('cancel')">{{ f('cancel') }}</NButton></div><p v-if="busy" role="status">{{ f('busy') }}</p>
    </section>
    <ConfirmImpactDialog v-model:show="dialog" :title="f('confirmTitle', { action: actionLabel })" :impacts="impacts" :busy="busy" @confirm="runAction" @cancel="action = undefined" />
  </template>
</template>

<style scoped>
.notice { margin-bottom: 20px; }.notice p { margin-bottom: 0; }.header-links, .source-links { display: flex; flex-wrap: wrap; gap: 12px 24px; }.header-links a { min-height: 44px; display: inline-flex; align-items: center; }.summary-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 24px; margin: 24px 0 32px; }dd { font-family: inherit; }h3 { font-size: 14px; margin: 24px 0 12px; }.result { display: inline-block; padding: 4px 8px; background: #eaf3ed; color: #1d6655; border-radius: 6px; font-size: 12px; }.result.warning { background: #fff3e6; color: #8a511a; }table { border-collapse: collapse; width: 100%; min-width: 850px; text-align: left; }th { font-size: 12px; color: var(--color-muted); font-weight: 500; background: #f7faf8; }td, th { padding: 16px 12px; border-bottom: 1px solid var(--color-border); vertical-align: top; }td:last-child { max-width: 300px; }small { display: block; margin-top: 6px; color: var(--color-muted); }.log-link { display: inline-flex; min-height: 44px; align-items: center; font-size: 12px; }.detail-columns { display: grid; grid-template-columns: 1fr 1fr; gap: 24px; }.stage-list, .dependency-list { padding-left: 24px; }.stage-list li, .dependency-list li { padding-left: 4px; margin: 0 0 20px; line-height: 1.7; }.stage-heading { display: flex; gap: 12px; flex-wrap: wrap; justify-content: space-between; }.stage-list p { margin: 4px 0; font-size: 12px; }.dependency-list li::marker { color: var(--color-primary); font-weight: 600; }.actions :deep(.n-button) { min-height: 44px; }
@media (max-width: 1050px) { .summary-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }.detail-columns { grid-template-columns: 1fr; gap: 0; } }@media (max-width: 500px) { .summary-grid { grid-template-columns: 1fr; }.actions { flex-direction: column; align-items: stretch; } }
</style>
