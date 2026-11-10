<script setup lang="ts">
import { computed, ref, watch, onBeforeUnmount } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NDatePicker, NInput, NModal, NSelect, NTabs, NTab, NTag, useMessage } from 'naive-ui'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import EntityLink from '../../components/EntityLink.vue'
import QueryStamp from '../../components/QueryStamp.vue'
import ConfirmImpactDialog from '../../components/ConfirmImpactDialog.vue'
import CheckEgressDrawer from '../egress/CheckEgressDrawer.vue'
import { pageLocation } from '../../app/router'
import { useWorkspace } from '../../demo/workspace'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
import type { Subscription, UsageSnapshot } from '../../domain'
import { buildPlan, clone, draftFor, effectiveState, renderConfig, downloadExample, usageTotal, type Finding } from './model'
import { deleteSubscription, extendSubscription, syncSubscription } from './lifecycle'
import ConfigPreview from './ConfigPreview.vue'
const route = useRoute(); const router = useRouter(); const { t } = useI18n(); const message = useMessage()
const tr = (key: string) => t(`features.subscriptions.${key}`)
const { workspace, commands, sessionSecrets } = useWorkspace()
const sub = computed(() => workspace.subscriptions.find(s => s.id === route.params.subscriptionId))
const state = computed(() => sub.value ? effectiveState(sub.value) : 'revoked')
const tabs = ['overview', 'entries', 'config', 'usage']
const tab = computed({ get: () => typeof route.query.tab === 'string' && tabs.includes(route.query.tab) ? route.query.tab : 'overview', set: value => { if (sub.value) void router.replace(pageLocation('subscription-detail', { subscriptionId: sub.value.id }, { ...route.query, tab: value })) } })
const draft = computed(() => sub.value ? draftFor(sub.value) : undefined)
const plan = computed(() => draft.value && sub.value ? buildPlan(workspace, draft.value, sub.value.id) : undefined)
const blocked = computed(() => !sub.value || state.value === 'expired' || state.value === 'revoked' || !!plan.value?.errors.length)
const entries = computed(() => workspace.entries.filter(e => e.subscriptionId === sub.value?.id))
const usageRows = computed(() => workspace.usage.filter(u => u.subscriptionId === sub.value?.id).slice().sort((a, b) => b.queriedAt.localeCompare(a.queriedAt)))
const total = computed(() => usageTotal(usageRows.value, sub.value?.entryIds ?? []))
const downloadUrl = computed(() => sub.value ? `https://example.invalid/subscriptions/${encodeURIComponent(sub.value.id)}?version=${sub.value.downloadVersion ?? 1}` : '')
const urlFallback = ref(false); const confirmation = ref<'reset' | 'delete' | undefined>()
const showExtend = ref(false); const newExpiry = ref<number | null>(null); const expiryError = ref(false)
const showCheck = ref(false); const selectedEntry = ref<string | null>(null)
const checkEntry = computed(() => entries.value.find(e => e.id === selectedEntry.value))
const task = useSimulatedTask()
const listenerFor = (id: string) => workspace.listeners.find(l => l.id === id)
const nodeFor = (listenerId: string) => workspace.nodes.find(n => n.id === listenerFor(listenerId)?.nodeId)
const offlineNodes = computed(() => [...new Set(entries.value.filter(e => e.cleanup !== 'confirmed' && nodeFor(e.listenerId)?.control !== 'online').map(e => nodeFor(e.listenerId)?.name ?? e.listenerId))])
const impacts = computed(() => confirmation.value === 'reset' ? [tr('resetImpact')] : [tr('deleteImpact'), t('features.subscriptions.entryCount', { count: entries.value.filter(e => e.cleanup !== 'confirmed').length }), tr('cleanupImpact'), ...(offlineNodes.value.length ? [t('features.subscriptions.offlineImpact', { nodes: offlineNodes.value.join(', ') })] : [])])
watch(() => sub.value?.id, (_id, oldId) => { if (oldId) for (const entry of workspace.entries.filter(e => e.subscriptionId === oldId)) sessionSecrets.delete(`subscription-entry:${entry.id}`); task.cancel(); confirmation.value = undefined; showExtend.value = false; showCheck.value = false; selectedEntry.value = sub.value?.entryIds[0] ?? null; urlFallback.value = false }, { immediate: true })
watch(() => route.query.panel, panel => { if (panel === 'preview') tab.value = 'config'; if (panel === 'check-egress' || panel === 'egress') showCheck.value = true }, { immediate: true })
function finding(item: Finding) { return `${tr(`validation.${item.key}`)}${item.detail ? ` · ${item.detail}` : ''}` }
function edit(step = 'basics') { if (sub.value) void router.push(pageLocation('subscription-edit', { subscriptionId: sub.value.id }, { step })) }
async function copyUrl() { if (blocked.value) return; try { await navigator.clipboard.writeText(downloadUrl.value); message.success(tr('copied')) } catch { urlFallback.value = true; message.warning(tr('copyFailed')) } }
function fullDownload() { if (blocked.value || !draft.value || !plan.value) return; downloadExample(renderConfig(workspace, draft.value, plan.value), 'meshstrata-full-example.yaml') }
function confirm() {
  if (!sub.value) return
  const source = clone<Subscription>(sub.value)
  if (confirmation.value === 'reset') { source.downloadVersion = (source.downloadVersion ?? 1) + 1; source.updatedAt = new Date().toISOString(); commands.upsert('subscriptions', source); message.success(tr('resetDone')) }
  if (confirmation.value === 'delete') { deleteSubscription(source); message.success(tr('deleteDone')) }
  confirmation.value = undefined
}
function extend() {
  if (!sub.value || newExpiry.value === null || !extendSubscription(clone<Subscription>(sub.value), new Date(newExpiry.value).toISOString())) { expiryError.value = true; return }
  showExtend.value = false; message.success(tr('extended'))
}
function sync() { if (sub.value) { const done = syncSubscription(clone<Subscription>(sub.value)); message[done ? 'success' : 'warning'](tr(done ? 'syncDone' : 'noOnline')) } }
function closeCheck(value: boolean) {
  showCheck.value = value
  if (!value && sub.value && route.query.panel) void router.replace(pageLocation('subscription-detail', { subscriptionId: sub.value.id }, { tab: tab.value }))
}
async function queryUsage() {
  if (!sub.value || task.state.value === 'running') return
  const subId = sub.value.id
  if (!await task.run(workspace.scenario === 'error' ? 'error' : 'success')) return
  if (sub.value?.id !== subId) return
  const now = new Date().toISOString()
  const currentEntries = entries.value.filter(e => sub.value?.entryIds.includes(e.id))
  currentEntries.forEach((entry, index) => {
    const isOnline = nodeFor(entry.listenerId)?.control === 'online'
    const metric = !isOnline ? 'unavailable' : workspace.scenario === 'partial' && index % 2 === 1 ? 'active_snapshot' : 'cumulative_bytes'
    const row: UsageSnapshot = { id: `usage-${entry.id}`, name: entry.name, updatedAt: now, subscriptionId: subId, entryId: entry.id, scope: 'entry', metric, bytes: metric === 'unavailable' ? null : (index + 1) * 4096, uploadBytes: metric === 'unavailable' ? null : (index + 1) * 1024, downloadBytes: metric === 'unavailable' ? null : (index + 1) * 3072, connections: metric === 'active_snapshot' ? index + 1 : null, coverageStart: metric === 'active_snapshot' ? now : '2026-11-10T00:00:00.000Z', coverageEnd: now, queriedAt: now, reset: false, complete: metric === 'cumulative_bytes' }
    commands.upsert('usage', row)
  })
  message.success(tr('queryDone'))
}
function usageValue(row: Readonly<UsageSnapshot>) { if (row.metric === 'unavailable' || (!row.metric && row.bytes === null)) return tr('unavailable'); if (row.metric === 'connection_count') return row.connections == null ? tr('unavailable') : t('features.subscriptions.connections', { value: row.connections.toLocaleString() }); return row.bytes === null ? tr('unknown') : t('features.subscriptions.bytes', { value: row.bytes.toLocaleString() }) }
function clearSecrets() { for (const entry of entries.value) sessionSecrets.delete(`subscription-entry:${entry.id}`) }
onBeforeUnmount(clearSecrets)
</script>
<template>
  <PageHeader :title="sub?.name ?? tr('notFound')" :description="tr('simulation')"><template #actions><RouterLink :to="pageLocation('subscriptions')">{{ tr('back') }}</RouterLink><NButton v-if="sub && state !== 'revoked'" @click="edit()">{{ tr('edit') }}</NButton></template></PageHeader>
  <EmptyState v-if="!sub" :title="tr('notFound')" :action-label="tr('back')" @action="router.push(pageLocation('subscriptions'))" />
  <template v-else>
    <NAlert v-if="state === 'revoked' || state === 'expired'" type="warning" class="notice">{{ tr(state === 'revoked' ? 'revokedNotice' : 'expiredNotice') }}</NAlert>
    <NTabs v-model:value="tab" type="line" class="detail-tabs"><NTab v-for="name in tabs" :key="name" :name="name">{{ tr(name) }}</NTab></NTabs>
    <section v-if="tab === 'overview'" class="panel">
      <div class="summary-grid"><div><dt>{{ tr('expiry') }}</dt><dd>{{ new Date(sub.expiresAt).toLocaleString() }} <NTag size="small" :type="state === 'active' ? 'success' : 'warning'">{{ tr(state) }}</NTag></dd></div><div><dt>{{ tr('sync') }}</dt><dd>{{ tr(sub.sync) }}</dd></div><div><dt>{{ tr('profile') }}</dt><dd><EntityLink kind="profile" :id="sub.profileId" :label="workspace.profiles.find(p => p.id === sub?.profileId)?.name ?? sub.profileId" /></dd></div><div><dt>{{ tr('downloadVersion') }}</dt><dd>{{ sub.downloadVersion ?? 1 }}</dd></div></div>
      <p>{{ sub.description }}</p><p>{{ tr('identityHint') }}</p>
      <h2>{{ tr('groups') }}</h2><div v-for="group in plan?.groups ?? sub.groups" :key="group.name" class="group"><strong>{{ group.name }}</strong><p>{{ tr('members') }}: <template v-for="(routeId, index) in group.routeIds" :key="routeId"><span v-if="index"> → </span><EntityLink kind="route" :id="routeId" :label="workspace.routes.find(r => r.id === routeId)?.name ?? routeId" /></template></p><p>{{ tr('initial') }}: {{ workspace.routes.find(r => r.id === group.selectedRouteId)?.name ?? group.selectedRouteId }}</p></div>
      <div class="actions"><NButton :disabled="state === 'revoked'" @click="edit('policy')">{{ tr('profileChange') }} / {{ tr('groups') }}</NButton><NButton :disabled="state === 'revoked'" @click="expiryError = false; newExpiry = Math.max(Date.now(), Date.parse(sub.expiresAt)) + 30 * 86400000; showExtend = true">{{ tr('extend') }}</NButton></div>
      <h2 class="section-heading">{{ tr('url') }}</h2><NInput :value="state === 'revoked' || state === 'expired' ? tr(state) : downloadUrl" readonly :aria-label="tr('url')" @focus="($event.target as HTMLInputElement)?.select()" /><p>{{ tr('urlHint') }}</p><p v-if="urlFallback" role="status">{{ tr('copyFailed') }}</p>
      <div class="actions"><NButton type="primary" :disabled="blocked" @click="copyUrl">{{ tr('copyUrl') }}</NButton><NButton :disabled="blocked" @click="fullDownload">{{ tr('download') }}</NButton><NButton @click="tab = 'config'">{{ tr('preview') }}</NButton><NButton :disabled="state === 'revoked' || state === 'expired'" @click="confirmation = 'reset'">{{ tr('reset') }}</NButton></div>
      <div class="danger-area"><NButton type="error" secondary :disabled="state === 'revoked'" @click="confirmation = 'delete'">{{ tr('delete') }}</NButton><NButton v-if="sub.sync === 'pending' || state === 'expired' || entries.some(e => e.cleanup === 'pending')" @click="sync">{{ tr('syncNow') }}</NButton><RouterLink :to="pageLocation('activity', {}, { tab: 'events', entityType: 'subscription', entityId: sub.id })">{{ tr('activity') }}</RouterLink></div>
    </section>
    <section v-if="tab === 'entries'" class="panel">
      <h2>{{ tr('entries') }} · {{ entries.length }}</h2><p>{{ tr('identityHint') }}</p><p>{{ tr('cleanupImpact') }}</p>
      <div v-if="entries.length" class="table-scroll"><table><thead><tr><th>{{ tr('entryId') }}</th><th>{{ tr('endpoint') }}</th><th>{{ tr('identity') }}</th><th>{{ tr('expiry') }}</th><th>{{ tr('cleanup') }}</th><th>{{ tr('checkEgress') }}</th></tr></thead><tbody><tr v-for="entry in entries" :key="entry.id"><td><code>{{ entry.id }}</code><br /><EntityLink kind="route" :id="entry.routeId" :label="workspace.routes.find(r => r.id === entry.routeId)?.name ?? entry.routeId" /></td><td><EntityLink v-if="nodeFor(entry.listenerId)" kind="node" :id="nodeFor(entry.listenerId)!.id" :label="nodeFor(entry.listenerId)!.name" /><br />{{ nodeFor(entry.listenerId)?.address ?? tr('unknown') }}:{{ entry.port }}<small>{{ listenerFor(entry.listenerId)?.protocol ?? tr('unknown') }} · {{ tr('isolated') }}</small><small>{{ tr('listener') }}: {{ entry.listenerId }}</small></td><td><NTag size="small" :type="entry.identityState === 'active' && state !== 'expired' && state !== 'revoked' ? 'success' : 'warning'">{{ tr(entry.identityState) }}</NTag><small v-if="state === 'expired'">{{ tr('expired') }}</small></td><td>{{ new Date(entry.expiresAt).toLocaleString() }}</td><td>{{ tr(entry.cleanup === 'pending' ? 'pendingCleanup' : entry.cleanup ?? 'none') }}</td><td><NButton :disabled="!workspace.routes.some(r => r.id === entry.routeId) || entry.identityState === 'revoked'" @click="selectedEntry = entry.id; showCheck = true">{{ tr('checkEgress') }}</NButton></td></tr></tbody></table></div><EmptyState v-else :title="tr('noMatches')" />
      <NButton v-if="sub.sync === 'pending'" @click="sync">{{ tr('syncNow') }}</NButton>
    </section>
    <section v-if="tab === 'config' && plan && draft" class="panel">
      <h2>{{ tr('config') }}</h2>
      <NAlert v-if="plan.errors.length" type="error" class="notice" :title="tr('errors')"><ul><li v-for="(error, index) in plan.errors" :key="index">{{ finding(error) }}</li></ul><NButton :disabled="state === 'revoked'" @click="edit('review')">{{ tr('edit') }}</NButton></NAlert>
      <NAlert type="warning" class="notice" :title="tr('warnings')"><ul><li v-for="(warning, index) in plan.warnings" :key="index">{{ finding(warning) }}</li></ul></NAlert>
      <ConfigPreview :draft="draft" :plan="plan" :blocked="blocked" />
      <div class="actions"><NSelect v-model:value="selectedEntry" :options="entries.filter(e => sub?.entryIds.includes(e.id)).map(e => ({ label: e.name, value: e.id }))" :placeholder="tr('checkEntry')" :aria-label="tr('checkEntry')" style="width: min(360px, 100%)" /><NButton :disabled="!checkEntry || !workspace.routes.some(r => r.id === checkEntry?.routeId)" @click="showCheck = true">{{ tr('checkEgress') }}</NButton></div>
    </section>
    <section v-if="tab === 'usage'" class="panel">
      <div class="usage-header"><div><h2>{{ tr('total') }}</h2><strong class="total">{{ total.value === null ? tr('partial') : t(`features.subscriptions.${total.metric === 'connection_count' ? 'connections' : 'bytes'}`, { value: total.value.toLocaleString() }) }}</strong></div><div class="actions"><NButton type="primary" :loading="task.state.value === 'running'" @click="queryUsage">{{ tr('queryUsage') }}</NButton><NButton v-if="task.state.value === 'running'" @click="task.cancel">{{ tr('cancelQuery') }}</NButton></div></div><p>{{ tr('usageHint') }}</p>
      <NAlert v-if="task.state.value === 'error'" type="error" class="notice">{{ tr('queryFailed') }} <NButton text @click="queryUsage">{{ tr('retry') }}</NButton></NAlert>
      <p v-if="task.state.value === 'running'" role="status">{{ tr('querying') }}</p>
      <EmptyState v-if="!usageRows.length" :title="tr('notQueried')" :action-label="tr('queryUsage')" @action="queryUsage" />
      <div v-else class="table-scroll"><table><thead><tr><th>{{ tr('entryId') }} / {{ tr('scope') }}</th><th>{{ tr('metric') }}</th><th>{{ tr('usage') }}</th><th>{{ tr('coverage') }}</th><th>{{ tr('queriedAt') }}</th></tr></thead><tbody><tr v-for="row in usageRows" :key="row.id"><td><code>{{ row.entryId ?? '—' }}</code><small>{{ row.scope }}</small></td><td>{{ tr(row.metric ?? 'unavailable') }}</td><td>{{ usageValue(row) }}<small v-if="row.metric === 'active_snapshot'">{{ t('features.subscriptions.connections', { value: row.connections ?? '—' }) }}</small><small v-if="row.uploadBytes != null || row.downloadBytes != null">↑ {{ row.uploadBytes?.toLocaleString() ?? '—' }} / ↓ {{ row.downloadBytes?.toLocaleString() ?? '—' }}</small></td><td>{{ new Date(row.coverageStart).toLocaleString() }} → {{ new Date(row.coverageEnd).toLocaleString() }}<small>{{ tr(row.complete ? 'complete' : 'incomplete') }} <span v-if="row.reset">· {{ tr('resetCounter') }}</span></small></td><td><QueryStamp :queried-at="row.queriedAt" :expires-at="new Date(Date.parse(row.queriedAt) + 300000).toISOString()" :scope="row.scope" /></td></tr></tbody></table></div>
    </section>
    <ConfirmImpactDialog :show="!!confirmation" :title="tr(confirmation === 'reset' ? 'resetTitle' : 'deleteTitle')" :impacts="impacts" @update:show="value => { if (!value) confirmation = undefined }" @confirm="confirm" @cancel="confirmation = undefined" />
    <NModal v-model:show="showExtend" preset="card" :title="tr('extend')" style="width: min(560px, 92vw)"><p>{{ t('features.subscriptions.entryCount', { count: sub.entryIds.length }) }}</p><p v-if="offlineNodes.length">{{ t('features.subscriptions.offlineImpact', { nodes: offlineNodes.join(', ') }) }}</p><NDatePicker v-model:value="newExpiry" type="datetime" :aria-label="tr('expiry')" style="width: 100%" /><p v-if="expiryError" role="alert">{{ tr('extendInvalid') }}</p><div class="actions"><NButton @click="showExtend = false">{{ tr('cancel') }}</NButton><NButton type="primary" @click="extend">{{ tr('extendSave') }}</NButton></div></NModal>
    <CheckEgressDrawer v-if="checkEntry && workspace.routes.some(r => r.id === checkEntry?.routeId)" :show="showCheck" :route-id="checkEntry.routeId" :subscription-id="sub.id" :entry-id="checkEntry.id" scope="route" @update:show="closeCheck" @completed="message.success(tr('checksDone'))" />
  </template>
</template>
<style scoped>
.detail-tabs { margin-bottom: 24px; }.notice { margin-bottom: 20px; }.summary-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 28px; margin: 8px 0 28px; }.summary-grid dd { line-height: 1.8; }.group { border-bottom: 1px solid var(--color-border); padding: 16px 0; }.group p { margin: 8px 0; }.section-heading { margin-top: 32px; }.danger-area { display: flex; flex-wrap: wrap; align-items: center; gap: 16px; margin-top: 32px; padding-top: 24px; border-top: 1px solid var(--color-border); }.usage-header { display: flex; flex-wrap: wrap; gap: 24px; align-items: center; justify-content: space-between; }.total { display: block; color: var(--color-primary); font-size: 20px; max-width: 600px; }table { width: 100%; min-width: 860px; border-collapse: collapse; margin: 20px 0; }th, td { text-align: left; padding: 16px 12px; border-bottom: 1px solid var(--color-border); vertical-align: top; }th { color: var(--color-muted); font-weight: 500; }small { display: block; font-size: 12px; color: var(--color-muted); margin-top: 8px; }code { font-size: 12px; }
@media (max-width: 760px) { .summary-grid { grid-template-columns: 1fr; }.total { font-size: 17px; } }
</style>
