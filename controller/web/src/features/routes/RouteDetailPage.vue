<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useDisplay } from '../../composables/useDisplay'
import { useI18n } from 'vue-i18n'
import { NButton, NAlert, NTabs, NTabPane, NModal, NSelect, useMessage } from 'naive-ui'
import PageHeader from '../../components/PageHeader.vue'
import EntityLink from '../../components/EntityLink.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import QueryStamp from '../../components/QueryStamp.vue'
import EmptyState from '../../components/EmptyState.vue'
import ConfirmImpactDialog from '../../components/ConfirmImpactDialog.vue'
import { resetEpoch, useWorkspace } from '../../demo/workspace'
import { useFormat } from '../../composables/useFormat'
import { pageLocation } from '../../app/router'
import type { Draft, Release, Route } from '../../domain'
import EgressPolicyDrawer from '../egress/EgressPolicyDrawer.vue'
import CheckEgressDrawer from '../egress/CheckEgressDrawer.vue'
import DuplicateRouteDialog from './DuplicateRouteDialog.vue'
import RouteTopology from './RouteTopology.vue'
import RouteMeasurements from './RouteMeasurements.vue'
import RouteResources from './RouteResources.vue'
import { canUseRoute, clone, configState, displayedRevision, editorRevision, nodeName, referencedSubscriptions, revisionChanged, routeResources, savedRouteDraft, udpCapability } from './route-model'
const { t } = useI18n(); const current = useRoute(); const router = useRouter(); const { workspace, commands } = useWorkspace(); const { date } = useFormat(); const message = useMessage()
const { display } = useDisplay()
const prefix = 'features.routes.'
const route = computed(() => workspace.routes.find(item => item.id === String(current.params.routeId)))
const revision = computed(() => route.value ? displayedRevision(route.value) : undefined)
const savedDraft = computed(() => route.value ? savedRouteDraft(workspace, route.value.id) : undefined)
const draftRevision = computed(() => route.value ? editorRevision(workspace, route.value) : undefined)
const terminalId = computed(() => revision.value?.nodeIds.at(-1) ?? '')
const policy = computed(() => workspace.policies.find(item => item.id === revision.value?.policyId && item.revision === revision.value?.policyRevision))
const subscriptions = computed(() => route.value ? referencedSubscriptions(workspace, route.value.id) : [])
const resources = computed(() => route.value ? routeResources(workspace, route.value) : undefined)
const checks = computed(() => workspace.checks.filter(item => item.routeId === route.value?.id).slice().sort((a, b) => b.observedAt.localeCompare(a.observedAt)))
const lastCheck = computed(() => checks.value[0])
const lastEgress = computed(() => checks.value.find(item => item.kind === 'egress_ip' && item.scope === 'route' && !item.subscriptionId && !item.entryId && item.status === 'observed' && item.revision === revision.value?.revision && item.policyId === revision.value?.policyId))
const releases = computed(() => workspace.releases.filter(item => item.routeId === route.value?.id).slice().sort((a, b) => b.updatedAt.localeCompare(a.updatedAt)))
const events = computed(() => workspace.events.filter(item => item.entityType === 'route' && item.entityId === route.value?.id).slice().sort((a, b) => b.updatedAt.localeCompare(a.updatedAt)))
const tab = computed({ get: () => typeof current.query.tab === 'string' ? current.query.tab : 'overview', set: value => { if (route.value) void router.replace(pageLocation('route-detail', { routeId: route.value.id }, { ...current.query, tab: value })) } })
function panel(show: boolean, name: 'egress' | 'check-egress') { if (route.value) void router.replace(pageLocation('route-detail', { routeId: route.value.id }, { ...current.query, panel: show ? name : undefined })) }
const showEgress = computed({ get: () => current.query.panel === 'egress', set: show => panel(show, 'egress') })
const showCheck = computed({ get: () => current.query.panel === 'check-egress', set: show => panel(show, 'check-egress') })
const copyId = ref(''); const showCopy = ref(false)
function duplicate() { copyId.value = `route-copy-${crypto.randomUUID()}`; showCopy.value = true }
function edit() { if (route.value) void router.push(pageLocation('route-edit', { routeId: route.value.id }, { step: 'options', draftId: savedDraft.value?.id })) }
const showSubscription = ref(false); const selectedSubscription = ref<string | null>(null)
const eligible = computed(() => route.value ? canUseRoute(workspace, route.value) : false)
const subscriptionOptions = computed(() => workspace.subscriptions.map(item => ({ value: item.id, label: `${item.name} · ${t(`${prefix}subscriptionStates.${item.state}`)}${item.routeIds.includes(route.value?.id ?? '') ? ` · ${t(`${prefix}alreadyIncluded`)}` : ''}`, disabled: item.state === 'revoked' || item.state === 'expired' || Date.parse(item.expiresAt) <= Date.now() })))
function chooseSubscription() {
  if (!route.value || !eligible.value) return
  selectedSubscription.value = null; showSubscription.value = true
}
function newSubscription() { if (route.value && eligible.value) { showSubscription.value = false; void router.push(pageLocation('subscription-new', {}, { routeIds: route.value.id, step: 'basics' })) } }
function existingSubscription() {
  const source = route.value; const subscription = workspace.subscriptions.find(item => item.id === selectedSubscription.value)
  if (!source || !eligible.value || !subscription || ['expired', 'revoked'].includes(subscription.state) || Date.parse(subscription.expiresAt) <= Date.now()) return
  const existing = workspace.drafts.find(draft => draft.feature === 'subscriptions' && draft.objectId === subscription.id)
  const draftId = existing?.id ?? `subscription-route-${crypto.randomUUID()}`
  if (existing) {
    const edited = clone<Draft>(existing)
    edited.routeIds = [...new Set([...edited.routeIds, source.id])]
    edited.step = 'routes'
    commands.saveDraft(edited)
  } else commands.saveDraft({ id: draftId, feature: 'subscriptions', objectId: subscription.id, name: subscription.name, nodeIds: [], routeIds: [...new Set([...subscription.routeIds, source.id])], profileId: subscription.profileId, step: 'routes', updatedAt: new Date().toISOString(), data: { subscription: { description: subscription.description ?? '', expiresAt: subscription.expiresAt, grouping: clone(subscription.grouping ?? { allRoutes: true, byExit: true }), client: clone(subscription.client ?? { mode: 'tun', ipv6: false, allowBusinessUdp: false }), selections: subscription.groups.map(group => ({ group: group.name, routeId: group.selectedRouteId })) } } })
  if (existing) message.info(t(`${prefix}resumeSubscription`))
  else message.success(t(`${prefix}subscriptionDraftSaved`))
  showSubscription.value = false
  void router.push(pageLocation('subscription-edit', { subscriptionId: subscription.id }, { draftId, step: 'routes' }))
}
const confirmAction = ref<'withdraw' | 'delete'>()
const blocked = computed(() => {
  if (!route.value) return true
  if (subscriptions.value.length || workspace.entries.some(entry => entry.routeId === route.value?.id)) return true
  return confirmAction.value === 'delete' && !!(resources.value?.listeners.some(item => item.routeIds.includes(route.value!.id)) || resources.value?.tunnels.some(item => item.routeIds.includes(route.value!.id)))
})
const impacts = computed(() => {
  const result = [t(`${prefix}${confirmAction.value === 'delete' ? 'deleteImpact' : 'withdrawImpact'}`), t(`${prefix}referenceImpact`, { count: subscriptions.value.length }), t(`${prefix}resourceImpact`, { listeners: resources.value?.listeners.length ?? 0, tunnels: resources.value?.tunnels.length ?? 0 })]
  if (blocked.value) result.push(t(`${prefix}blockedImpact`))
  else if (confirmAction.value === 'withdraw') result.push(t(`${prefix}pendingCleanupImpact`))
  return result
})
function act() {
  const source = route.value
  if (!source || blocked.value) return
  const now = new Date().toISOString()
  if (confirmAction.value === 'delete') {
    workspace.drafts.filter(draft => ['route', 'routes', 'route-editor'].includes(draft.feature) && draft.objectId === source.id).forEach(draft => commands.remove('drafts', draft.id))
    commands.remove('routes', source.id); confirmAction.value = undefined
    message.success(t(`${prefix}draftDeleted`)); void router.push(pageLocation('routes')); return
  }
  if (source.withdrawnAt) { confirmAction.value = undefined; return }
  const changed = clone<Route>(source); changed.withdrawnAt = now; changed.cleanup = 'pending'; changed.updatedAt = now
  commands.upsert('routes', changed)
  const release: Release = { id: `withdraw-${crypto.randomUUID()}`, name: t(`${prefix}withdrawTask`, { name: source.name }), updatedAt: now, kind: 'revoke', routeId: source.id, revision: source.published?.revision ?? source.draft.revision, state: 'applying', cleanup: 'pending', startedAt: now, nodes: (source.published ?? source.draft).nodeIds.map(nodeId => ({ nodeId, applied: false, verified: false, rollback: 'none' })) }
  commands.upsert('releases', release)
  commands.upsert('events', { id: `event-${release.id}`, name: release.name, updatedAt: now, kind: 'revoke', entityType: 'route', entityId: source.id, status: 'pending', releaseId: release.id })
  confirmAction.value = undefined; message.info(t(`${prefix}withdrawPending`)); void router.push(pageLocation('release-detail', { releaseId: release.id }))
}
function back() {
  const previous: unknown = window.history.state?.back
  if (typeof previous === 'string' && router.resolve(previous).name === 'routes') router.back()
  else void router.push(pageLocation('routes'))
}
watch(() => route.value?.id, () => { showCopy.value = false; showSubscription.value = false; confirmAction.value = undefined })
watch(resetEpoch, () => { showCopy.value = false; showSubscription.value = false; confirmAction.value = undefined })
</script>
<template>
  <div class="detail-page">
    <template v-if="route && revision && draftRevision">
      <div><NButton text @click="back">← {{ t(`${prefix}back`) }}</NButton></div>
      <PageHeader :title="route.name" :description="t(`${prefix}detailDescription`, { id: route.id })"><template #actions><NButton type="primary" @click="edit">{{ t(`${prefix}edit`) }}</NButton></template></PageHeader>
      <div class="actions detail-actions"><NButton :disabled="!eligible" @click="chooseSubscription">{{ t(`${prefix}addSubscription`) }}</NButton><NButton @click="tab = 'measurements'">{{ t(`${prefix}measure`) }}</NButton><NButton :disabled="!route.published || !!route.withdrawnAt" @click="showCheck = true">{{ t(`${prefix}checkEgress`) }}</NButton><NButton @click="duplicate">{{ t(`${prefix}duplicate`) }}</NButton><NButton v-if="route.published" :disabled="!!route.withdrawnAt" @click="confirmAction = 'withdraw'">{{ t(`${prefix}withdraw`) }}</NButton><NButton v-else @click="confirmAction = 'delete'">{{ t(`${prefix}deleteDraft`) }}</NButton></div>
      <NAlert v-if="!eligible" type="warning" :bordered="false">{{ t(`${prefix}cannotSubscribe`) }} <NButton text @click="edit">{{ t(`${prefix}reviewPublish`) }}</NButton></NAlert>
      <NAlert v-if="workspace.scenario === 'error'" type="error" :bordered="false">{{ t(`${prefix}queryError`) }}</NAlert>
      <div class="summary-layout">
        <section class="panel"><p class="version-label">{{ t(`${prefix}${route.published ? 'showingPublished' : 'showingDraft'}`) }}</p><RouteTopology :revision="revision" /></section>
        <aside class="panel summary"><h2>{{ t(`${prefix}configuration`) }}</h2><StatusBadge :status="configState(route)" :label="t(`${prefix}states.${configState(route)}`)" /><dl>
          <dt>{{ t(`${prefix}published`) }}</dt><dd>{{ route.published ? `r${route.published.revision}` : t(`${prefix}unpublished`) }}</dd><dt>{{ t(`${prefix}draft`) }}</dt><dd>r{{ draftRevision.revision }} · {{ t(`${prefix}${revisionChanged(route, draftRevision) ? 'draftChanged' : 'draftSame'}`) }}</dd>
          <dt>{{ t(`${prefix}terminal`) }}</dt><dd><EntityLink v-if="terminalId" kind="node" :id="terminalId" :label="nodeName(workspace, terminalId)" /><span v-else>{{ t(`${prefix}unknown`) }}</span></dd>
          <dt>{{ t(`${prefix}egressPolicy`) }}</dt><dd>{{ revision.policyId ? (policy ? `${policy.name} · r${revision.policyRevision}` : t(`${prefix}missingPolicy`, { id: revision.policyId, revision: revision.policyRevision ?? '?' })) : t(`${prefix}nodeDefault`) }}</dd>
          <dt>{{ t(`${prefix}observedEgress`) }}</dt><dd>{{ lastEgress?.value ?? t(`${prefix}notChecked`) }}<QueryStamp v-if="lastEgress" :queried-at="lastEgress.observedAt" :expires-at="lastEgress.expiresAt" :scope="lastEgress.testUrl" /></dd>
          <dt>{{ t(`${prefix}verification`) }}</dt><dd><StatusBadge :status="route.verification" /><QueryStamp :queried-at="route.verifiedAt" /></dd><dt>{{ t(`${prefix}recentCheck`) }}</dt><dd>{{ lastCheck ? t(`${prefix}checkStatuses.${lastCheck.status}`) : t(`${prefix}notChecked`) }}<QueryStamp :queried-at="lastCheck?.observedAt" :expires-at="lastCheck?.expiresAt" :scope="display('scope', lastCheck?.scope)" /></dd><dt>{{ t(`${prefix}updated`) }}</dt><dd>{{ date(route.updatedAt) }}</dd><dt>{{ t(`${prefix}cleanup`) }}</dt><dd>{{ t(`${prefix}cleanupStates.${route.cleanup}`) }}</dd>
        </dl><NButton :disabled="!terminalId" @click="showEgress = true">{{ t(`${prefix}viewEgress`) }}</NButton></aside>
      </div>
      <NTabs v-model:value="tab" type="line" animated>
        <NTabPane name="overview" :tab="t(`${prefix}tabs.overview`)"><div class="tab-content">
          <section class="panel"><h2>{{ t(`${prefix}capability`) }}</h2><p>{{ t(`${prefix}tcpDetail`) }}</p><p>{{ t(`${prefix}businessUdp`) }}: <strong>{{ t(`${prefix}udpStates.${udpCapability(workspace, revision)}`) }}</strong></p><p class="muted">{{ t(`${prefix}udpWarning`) }}</p><p>{{ t(`${prefix}udpRequest`, { value: t(`${prefix}${revision.requireUdpRelay ? 'yes' : 'no'}`) }) }}</p></section>
          <section class="panel"><h2>{{ t(`${prefix}revisions`) }}</h2><div class="revision-columns"><div><h3>{{ t(`${prefix}published`) }}</h3><template v-if="route.published"><p>r{{ route.published.revision }} · {{ route.published.nodeIds.map(id => nodeName(workspace, id)).join(' → ') }}</p><p>{{ route.published.description ?? t(`${prefix}noDescription`) }}</p><p class="mono">{{ route.published.entryListenerId }} · {{ route.published.policyId ?? t(`${prefix}nodeDefault`) }}</p></template><p v-else>{{ t(`${prefix}unpublished`) }}</p></div><div><h3>{{ t(`${prefix}draft`) }}</h3><p>r{{ draftRevision.revision }} · {{ draftRevision.nodeIds.map(id => nodeName(workspace, id)).join(' → ') }}</p><p>{{ draftRevision.description ?? t(`${prefix}noDescription`) }}</p><p class="mono">{{ draftRevision.entryListenerId }} · {{ draftRevision.policyId ?? t(`${prefix}nodeDefault`) }}</p><p>{{ t(`${prefix}${revisionChanged(route, draftRevision) ? 'draftChanged' : 'draftSame'}`) }}</p><p v-if="savedDraft">{{ t(`${prefix}savedEditorDraft`, { time: date(savedDraft.updatedAt) }) }}</p><NButton @click="edit">{{ t(`${prefix}editDraft`) }}</NButton></div></div></section>
          <section class="panel"><h2>{{ t(`${prefix}checkResults`) }}</h2><EmptyState v-if="!checks.length" :title="t(`${prefix}notChecked`)" :description="t(`${prefix}checkEmpty`)" /><article v-for="check in checks" :key="check.id" class="check"><strong>{{ t(`${prefix}checkKinds.${check.kind}`) }}</strong> · {{ t(`${prefix}checkStatuses.${check.status}`) }} · r{{ check.revision }}<p>{{ check.value ?? t(`${prefix}notChecked`) }}</p><p class="mono">{{ check.testUrl }}</p><QueryStamp :queried-at="check.observedAt" :expires-at="check.expiresAt" :scope="display('scope', check.scope)" /><p v-if="check.revision !== revision.revision" class="muted">{{ t(`${prefix}olderRevision`) }}</p><p v-if="check.scope === 'target_only'" class="muted">{{ t(`${prefix}targetOnlyWarning`) }}</p></article></section>
        </div></NTabPane>
        <NTabPane name="measurements" :tab="t(`${prefix}tabs.measurements`)"><RouteMeasurements :key="route.id" :route="route" /></NTabPane>
        <NTabPane name="resources" :tab="t(`${prefix}tabs.resources`)"><RouteResources :route="route" /></NTabPane>
        <NTabPane name="history" :tab="t(`${prefix}tabs.history`)"><div class="tab-content"><div class="actions"><RouterLink :to="pageLocation('activity', {}, { tab: 'releases', entityType: 'route', entityId: route.id })">{{ t(`${prefix}allHistory`) }}</RouterLink></div><EmptyState v-if="!releases.length && !events.length" :title="t(`${prefix}noHistory`)" :description="t(`${prefix}noHistoryHint`)" /><article v-for="release in releases" :key="release.id" class="panel"><EntityLink kind="release" :id="release.id" :label="release.name" /><p>{{ t(`${prefix}releaseKinds.${release.kind}`) }} · r{{ release.revision }} · <StatusBadge :status="release.state" /></p><p>{{ date(release.startedAt ?? release.updatedAt) }} · {{ t(`${prefix}cleanupStates.${release.cleanup}`) }}</p><p v-for="node in release.nodes" :key="node.nodeId"><EntityLink kind="node" :id="node.nodeId" :label="nodeName(workspace, node.nodeId)" /> · {{ t(`${prefix}appliedVerified`, { applied: t(`${prefix}${node.applied ? 'yes' : 'no'}`), verified: t(`${prefix}${node.verified ? 'yes' : 'no'}`) }) }} · {{ t(`${prefix}rollback`) }}: {{ t(`status.${node.rollback}`) }}</p></article><article v-for="event in events" :key="event.id" class="panel"><strong>{{ event.name }}</strong><p>{{ date(event.updatedAt) }} · {{ t(`status.${event.status}`) }}</p><EntityLink v-if="event.releaseId" kind="release" :id="event.releaseId" :label="t(`${prefix}openTask`)" /></article></div></NTabPane>
      </NTabs>
      <EgressPolicyDrawer v-model:show="showEgress" :key="`policy-${route.id}`" :route-id="route.id" :terminal-node-id="terminalId" :policy-id="revision.policyId" :readonly="true" />
      <CheckEgressDrawer v-if="route.published && !route.withdrawnAt" v-model:show="showCheck" :key="`check-${route.id}`" :route-id="route.id" scope="route" @completed="message.info(t(`${prefix}checksUpdated`))" />
      <DuplicateRouteDialog v-if="showCopy" :key="copyId" :source="route" :draft-id="copyId" @close="showCopy = false" />
      <NModal v-model:show="showSubscription" preset="card" :title="t(`${prefix}addSubscription`)" style="width: min(560px, 94vw)"><p>{{ t(`${prefix}subscriptionHint`) }}</p><label class="subscription-select">{{ t(`${prefix}existingSubscription`) }}<NSelect v-model:value="selectedSubscription" :options="subscriptionOptions" clearable :aria-label="t(`${prefix}existingSubscription`)" /></label><div class="actions"><NButton @click="showSubscription = false">{{ t('common.cancel') }}</NButton><NButton :disabled="!selectedSubscription || !eligible" @click="existingSubscription">{{ t(`${prefix}openSubscriptionDraft`) }}</NButton><NButton type="primary" :disabled="!eligible" @click="newSubscription">{{ t(`${prefix}newSubscription`) }}</NButton></div></NModal>
      <NModal v-if="confirmAction && blocked" :show="true" preset="card" :title="t(`${prefix}dependencyTitle`)" style="width: min(580px, 94vw)" @update:show="show => { if (!show) confirmAction = undefined }"><ul><li v-for="impact in impacts" :key="impact">{{ impact }}</li></ul><p v-for="subscription in subscriptions" :key="subscription.id"><EntityLink kind="subscription" :id="subscription.id" :label="subscription.name" /></p><div class="actions"><NButton @click="confirmAction = undefined">{{ t('common.cancel') }}</NButton><NButton @click="confirmAction = undefined; tab = 'resources'">{{ t(`${prefix}reviewReferences`) }}</NButton></div></NModal>
      <ConfirmImpactDialog :show="!!confirmAction && !blocked" :title="t(`${prefix}${confirmAction === 'delete' ? 'deleteDraft' : 'withdraw'}`)" :impacts="impacts" @update:show="show => { if (!show) confirmAction = undefined }" @cancel="confirmAction = undefined" @confirm="act" />
    </template>
    <EmptyState v-else :title="t('common.notFound')" :description="t(`${prefix}notFound`)" :action-label="t(`${prefix}back`)" @action="router.push(pageLocation('routes'))" />
  </div>
</template>
<style scoped>
.detail-page, .tab-content { grid-template-columns: minmax(0, 1fr); }
.detail-page > *, .tab-content > * { min-width: 0; }
.detail-page, .tab-content { display: grid; gap: 16px; }.detail-actions { flex-wrap: wrap; }.summary-layout { display: grid; grid-template-columns: minmax(0, 1fr) 320px; gap: 16px; align-items: start; }.summary h2 { margin-top: 0; }.summary dt { color: #63736a; margin-top: 14px; font-size: 12px; }.summary dd { margin: 4px 0 0; overflow-wrap: anywhere; }h2 { font-size: 17px; }h3 { font-size: 15px; }.revision-columns { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 24px; }.mono { font-family: ui-monospace, monospace; overflow-wrap: anywhere; }.muted { color: #63736a; font-size: 13px; }.check { border-top: 1px solid #e1e9e4; padding-top: 14px; margin-top: 14px; }.subscription-select { display: grid; gap: 8px; margin: 20px 0; }@media (max-width: 1000px) { .summary-layout { grid-template-columns: 1fr; } }@media (max-width: 600px) { .revision-columns { grid-template-columns: 1fr; }.detail-actions :deep(button) { min-height: 44px; } }
</style>
