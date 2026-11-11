<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { useFormat } from '../../composables/useFormat'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCheckbox, NDatePicker, NForm, NFormItem, NInput, NSelect, NSteps, NStep, NTag, useMessage } from 'naive-ui'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import EntityLink from '../../components/EntityLink.vue'
import CheckEgressDrawer from '../egress/CheckEgressDrawer.vue'
import { pageLocation, entityLocation, safeReturnTo } from '../../app/router'
import { useWorkspace } from '../../demo/workspace'
import { useDraft } from '../../composables/useDraft'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
import type { Draft } from '../../domain'
import { buildPlan, draftFor, formFor, id, isPublished, steps, type Finding } from './model'
import { saveSubscription } from './lifecycle'
import ConfigPreview from './ConfigPreview.vue'
const route = useRoute(); const router = useRouter(); const { date } = useFormat()
const { t } = useI18n(); const message = useMessage()
const tr = (key: string) => t(`features.subscriptions.${key}`)
const { workspace, commands } = useWorkspace()
const objectId = typeof route.params.subscriptionId === 'string' ? route.params.subscriptionId : undefined
const existing = computed(() => workspace.subscriptions.find(s => s.id === objectId))
const draftId = typeof route.query.draftId === 'string' ? route.query.draftId : objectId ? `subscription-draft-${objectId}` : id('subscription-draft')
const savedDraft = workspace.drafts.find(d => d.id === draftId)
const badDraft = computed(() => !!route.query.draftId && (!savedDraft || savedDraft.feature !== 'subscriptions' || savedDraft.objectId !== objectId))
const subscriptionId = objectId ?? id('sub')
const initial: Draft = existing.value ? { ...draftFor(existing.value), id: draftId } : { id: draftId, feature: 'subscriptions', name: '', routeIds: typeof route.query.routeIds === 'string' ? [...new Set(route.query.routeIds.split(',').filter(Boolean))] : [], nodeIds: [], profileId: typeof route.query.profileId === 'string' ? route.query.profileId : undefined, step: 'basics', updatedAt: new Date().toISOString(), data: { subscription: formFor() } }
const { value: draft, dirty, save: saveDraft, discard } = useDraft(draftId, initial)
if (!draft.value.data?.subscription) draft.value.data = { ...draft.value.data, subscription: formFor(existing.value) }
const form = computed(() => draft.value.data!.subscription!)
if (typeof route.query.profileId === 'string') draft.value.profileId = route.query.profileId
const step = computed(() => steps.find(s => s === route.query.step) ?? steps.find(s => s === draft.value.step) ?? 'basics')
watch(step, value => { draft.value.step = value }, { immediate: true })
const stepIndex = computed(() => steps.indexOf(step.value))
const plan = computed(() => buildPlan(workspace, draft.value, subscriptionId))
const visibleErrors = ref(false)
const stepErrors = computed(() => plan.value.errors.filter(e => step.value === 'review' || e.field === step.value))
const routeSearch = ref('')
const available = computed(() => workspace.routes.filter(r => isPublished(r) && !draft.value.routeIds.includes(r.id) && `${r.name} ${r.id}`.toLowerCase().includes(routeSearch.value.toLowerCase())))
const profileOptions = computed(() => workspace.profiles.map(p => ({ label: p.name, value: p.id })))
const expiry = computed({ get: () => { const date = Date.parse(form.value.expiresAt); return Number.isFinite(date) ? date : null }, set: (value: number | null) => { form.value.expiresAt = value === null ? '' : new Date(value).toISOString() } })
const task = useSimulatedTask()
const showCheck = ref(false); const checkRouteId = ref('')
const missing = computed(() => !!objectId && !existing.value)
const blocked = computed(() => missing.value || badDraft.value || existing.value?.state === 'revoked')
const added = computed(() => draft.value.routeIds.filter(r => !existing.value?.routeIds.includes(r)))
const removed = computed(() => existing.value?.routeIds.filter(r => !draft.value.routeIds.includes(r)) ?? [])
function finding(e: Finding) { return `${tr(`validation.${e.key}`)}${e.detail ? ` · ${e.detail}` : ''}` }
function goStep(index: number) {
  if (index > stepIndex.value && stepErrors.value.length) { visibleErrors.value = true; return }
  const target = steps[index]; if (!target) return
  draft.value.step = target; visibleErrors.value = false
  void router.replace(pageLocation(objectId ? 'subscription-edit' : 'subscription-new', objectId ? { subscriptionId: objectId } : {}, { ...route.query, step: target }))
}
function move(index: number, direction: number) { const list = [...draft.value.routeIds]; const target = index + direction; if (target < 0 || target >= list.length) return; [list[index], list[target]] = [list[target]!, list[index]!]; draft.value.routeIds = list }
function selectInitial(group: string, routeId: string) { const selection = form.value.selections.find(s => s.group === group); if (selection) selection.routeId = routeId; else form.value.selections.push({ group, routeId }) }
async function persistDraft() { saveDraft(); message.success(tr('draftSaved')); await router.replace(pageLocation(objectId ? 'subscription-edit' : 'subscription-new', objectId ? { subscriptionId: objectId } : {}, { ...route.query, draftId })) }
function profileEditor(create: boolean) {
  saveDraft()
  const location = pageLocation(objectId ? 'subscription-edit' : 'subscription-new', objectId ? { subscriptionId: objectId } : {}, { step: 'policy', draftId })
  const returnTo = safeReturnTo(router.resolve(location).fullPath)
  void router.push(pageLocation(create ? 'profile-new' : 'profile-edit', create ? {} : { profileId: draft.value.profileId ?? '' }, { draftId, returnTo, ...(objectId ? { subscriptionId: objectId } : {}) }))
}
async function submit() {
  visibleErrors.value = true
  if (blocked.value || plan.value.errors.length) return
  if (!await task.run(workspace.scenario === 'error' ? 'error' : 'success')) return
  const result = saveSubscription(draft.value, subscriptionId)
  if (!result.saved) { message.error(tr('noConfig')); return }
  // Save baseline to satisfy the dirty guard, then remove the completed draft.
  saveDraft(); commands.remove('drafts', draftId)
  message.success(tr('saved')); await router.push(entityLocation('subscription', result.saved.id))
}
function routeName(id: string) { return workspace.routes.find(r => r.id === id)?.name ?? id }
function routePath(id: string) { return workspace.routes.find(r => r.id === id)?.published?.nodeIds ?? [] }
</script>
<template>
  <PageHeader :title="tr(objectId ? 'edit' : 'create')" :description="tr('simulation')"><template #actions><RouterLink :to="pageLocation('subscriptions')">{{ tr('back') }}</RouterLink></template></PageHeader>
  <EmptyState v-if="missing || badDraft" :title="tr(missing ? 'notFound' : 'draft')" :description="badDraft ? tr('draftMissing') : undefined" :action-label="tr('back')" @action="router.push(pageLocation('subscriptions'))" />
  <NAlert v-else-if="existing?.state === 'revoked'" type="warning">{{ tr('revokedNotice') }}</NAlert>
  <template v-else>
    <section class="panel step-panel"><NSteps :current="stepIndex + 1" size="small"><NStep v-for="(name, index) in steps" :key="name" :title="tr(name)" @click="goStep(index)" /></NSteps><div class="step-nav"><NButton v-for="(name, index) in steps" :key="name" :type="step === name ? 'primary' : 'default'" @click="goStep(index)">{{ index + 1 }}. {{ tr(name) }}</NButton></div></section>
    <NAlert v-if="(visibleErrors || step === 'review') && stepErrors.length" type="error" class="notice" :title="tr('errors')"><ul><li v-for="(error, index) in stepErrors" :key="index">{{ finding(error) }} <NButton v-if="step === 'review' && error.field !== 'review'" size="small" text @click="goStep(steps.findIndex(s => s === error.field))">{{ t('features.subscriptions.goTo', { step: tr(error.field ?? 'review') }) }}</NButton></li></ul></NAlert>
    <section class="panel">
      <NForm v-if="step === 'basics'" label-placement="top" class="form-content" @submit.prevent>
        <NFormItem :label="tr('name')" label-for="subscription-name" required><NInput v-model:value="draft.name" :maxlength="100" :input-props="{ id: 'subscription-name', 'aria-label': tr('name') }" /></NFormItem>
        <NFormItem :label="tr('description')" label-for="subscription-description"><NInput v-model:value="form.description" type="textarea" :maxlength="1000" :input-props="{ id: 'subscription-description', 'aria-label': tr('description') }" /></NFormItem>
        <NFormItem :label="tr('expiry')" required><NDatePicker v-model:value="expiry" type="datetime" :aria-label="tr('expiry')" style="width: 100%" /></NFormItem><p>{{ tr('expiryHint') }}</p>
      </NForm>
      <template v-if="step === 'routes'">
        <h2>{{ tr('selected') }} · {{ draft.routeIds.length }}</h2><p>{{ tr('routeHint') }}</p>
        <ol class="route-list"><li v-for="(routeId, index) in draft.routeIds" :key="routeId"><div><EntityLink kind="route" :id="routeId" :label="routeName(routeId)" /><p>{{ tr('path') }}: <template v-for="(nodeId, n) in routePath(routeId)" :key="nodeId"><span v-if="n"> → </span><EntityLink kind="node" :id="nodeId" :label="workspace.nodes.find(node => node.id === nodeId)?.name ?? nodeId" /></template></p><NTag size="small">{{ tr('capability') }}: {{ tr('rejected') }}</NTag></div><div class="actions"><NButton :disabled="index === 0" :aria-label="t('features.subscriptions.moveUp', { name: routeName(routeId) })" @click="move(index, -1)">↑</NButton><NButton :disabled="index === draft.routeIds.length - 1" :aria-label="t('features.subscriptions.moveDown', { name: routeName(routeId) })" @click="move(index, 1)">↓</NButton><NButton @click="draft.routeIds = draft.routeIds.filter(id => id !== routeId)">{{ tr('remove') }}</NButton></div></li></ol>
        <h2>{{ tr('available') }}</h2><NInput v-model:value="routeSearch" clearable :placeholder="tr('search')" :aria-label="tr('search')" />
        <div class="available-routes"><div v-for="item in available" :key="item.id" class="route-choice"><EntityLink kind="route" :id="item.id" :label="item.name" /><NButton @click="draft.routeIds.push(item.id)">{{ tr('add') }}</NButton></div></div>
        <p v-if="!available.length">{{ tr('noMatches') }}</p><RouterLink v-if="!workspace.routes.some(isPublished)" :to="pageLocation('route-new')">{{ tr('createRoute') }}</RouterLink>
      </template>
      <template v-if="step === 'policy'">
        <NFormItem :label="tr('profile')" required><NSelect v-model:value="draft.profileId" :options="profileOptions" :placeholder="tr('profile')" :aria-label="tr('profile')" /></NFormItem>
        <div class="actions"><NButton @click="profileEditor(true)">{{ tr('createProfile') }}</NButton><NButton :disabled="!workspace.profiles.some(p => p.id === draft.profileId)" @click="profileEditor(false)">{{ tr('editProfile') }}</NButton></div><p>{{ tr('returnHint') }}</p>
        <details><summary>{{ tr('previewProfile') }}</summary><ol><li v-for="rule in workspace.profiles.find(p => p.id === draft.profileId)?.rules" :key="rule.id"><code>{{ [rule.type, ...(rule.type === 'MATCH' ? [] : [rule.value]), rule.target, ...(rule.options ?? [])].join(',') }}</code></li></ol></details>
        <div class="toggles"><NCheckbox v-model:checked="form.grouping.allRoutes">{{ tr('allRoutes') }}</NCheckbox><NCheckbox v-model:checked="form.grouping.byExit">{{ tr('byExit') }}</NCheckbox></div><p>{{ tr('customHint') }}</p>
        <div v-for="group in plan.groups" :key="group.name" class="group"><strong>{{ group.name }}</strong><p>{{ tr('members') }}: {{ group.routeIds.map(routeName).join(' → ') }}</p><NFormItem :label="tr('initial')"><NSelect :value="form.selections.find(s => s.group === group.name)?.routeId ?? group.selectedRouteId" :options="group.routeIds.map(r => ({ label: routeName(r), value: r }))" :aria-label="`${group.name} ${tr('initial')}`" @update:value="value => selectInitial(group.name, value)" /></NFormItem></div>
        <p v-if="!plan.groups.length">{{ tr('noGroups') }}</p>
        <h2>{{ tr('client') }}</h2><NFormItem :label="tr('networkMode')"><NSelect v-model:value="form.client.mode" :options="[{ label: tr('tun'), value: 'tun' }, { label: tr('systemProxy'), value: 'system_proxy' }]" :aria-label="tr('networkMode')" /></NFormItem><div class="toggles"><NCheckbox v-model:checked="form.client.ipv6">{{ tr('ipv6') }}</NCheckbox><NCheckbox v-model:checked="form.client.allowBusinessUdp">{{ tr('udp') }}</NCheckbox></div><p>{{ tr('advancedHint') }}</p>
      </template>
      <template v-if="step === 'review'">
        <h2>{{ draft.name }}</h2><p>{{ tr('reviewHint') }}</p><p>{{ tr('identityHint') }}</p>
        <dl class="review-grid"><div><dt>{{ tr('expiry') }}</dt><dd>{{ date(form.expiresAt) }}</dd></div><div><dt>{{ tr('profile') }}</dt><dd>{{ workspace.profiles.find(p => p.id === draft.profileId)?.name ?? tr('unknown') }}</dd></div></dl>
        <div v-if="existing" class="group"><h2>{{ tr('diff') }}</h2><p>{{ tr('addedRoutes') }}: {{ added.map(routeName).join(', ') || '—' }}</p><p>{{ tr('removedRoutes') }}: {{ removed.map(routeName).join(', ') || '—' }}</p><p>{{ tr('profileChange') }}: {{ existing.profileId }} → {{ draft.profileId }}</p></div>
        <div class="table-scroll"><table><thead><tr><th>{{ tr('entryId') }}</th><th>{{ tr('endpoint') }}</th><th>{{ tr('protocol') }}</th><th>{{ tr('credentials') }}</th></tr></thead><tbody><tr v-for="item in plan.entries" :key="item.entry.id"><td><code>{{ item.entry.id }}</code><br /><EntityLink kind="route" :id="item.entry.routeId" :label="routeName(item.entry.routeId)" /></td><td>{{ item.server }}:{{ item.entry.port }}</td><td>{{ item.listener.protocol }}</td><td>{{ tr(item.reused ? 'reused' : 'allocated') }}</td></tr></tbody></table></div>
        <NAlert v-if="!plan.errors.length" type="success">{{ tr('validationOk') }}</NAlert>
        <NAlert type="warning" class="notice" :title="tr('warnings')"><ul><li v-for="(warning, index) in plan.warnings" :key="index">{{ finding(warning) }}</li></ul></NAlert>
        <ConfigPreview :draft="draft" :plan="plan" />
        <div class="actions"><NSelect v-model:value="checkRouteId" :options="draft.routeIds.map(r => ({ label: routeName(r), value: r }))" :placeholder="tr('checkEntry')" :aria-label="tr('checkEntry')" style="width: min(320px, 100%)" /><NButton :disabled="!checkRouteId || !workspace.routes.some(r => r.id === checkRouteId)" @click="showCheck = true">{{ tr('checkEgress') }}</NButton></div>
      </template>
      <NAlert v-if="task.state.value === 'error'" type="error" class="notice">{{ tr('queryFailed') }}</NAlert>
      <div class="actions footer-actions"><NButton v-if="stepIndex > 0" :disabled="task.state.value === 'running'" @click="goStep(stepIndex - 1)">{{ tr('previous') }}</NButton><NButton :disabled="task.state.value === 'running'" @click="persistDraft">{{ tr('saveDraft') }}</NButton><NButton :disabled="!dirty || task.state.value === 'running'" @click="discard">{{ tr('discard') }}</NButton><RouterLink :to="pageLocation('subscriptions')">{{ tr('cancel') }}</RouterLink><NButton v-if="stepIndex < 3" type="primary" @click="goStep(stepIndex + 1)">{{ tr('next') }}</NButton><NButton v-else type="primary" :loading="task.state.value === 'running'" :disabled="!!plan.errors.length" @click="submit">{{ tr(objectId ? 'save' : 'generate') }}</NButton><NButton v-if="task.state.value === 'running'" @click="task.cancel">{{ tr('cancel') }}</NButton></div>
    </section>
    <CheckEgressDrawer v-if="checkRouteId" v-model:show="showCheck" :route-id="checkRouteId" :subscription-id="objectId" scope="route" />
  </template>
</template>
<style scoped>
.notice { margin-bottom: 20px; } .step-nav { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 20px; }.route-list { padding: 0; list-style: none; }.route-list li { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 16px; padding: 20px 0; border-bottom: 1px solid var(--color-border); }.route-list p { margin: 8px 0; }.available-routes { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin-top: 16px; }.route-choice { display: flex; align-items: center; justify-content: space-between; gap: 12px; border: 1px solid var(--color-border); border-radius: 8px; padding: 16px; }.toggles { display: flex; flex-wrap: wrap; gap: 20px; margin: 28px 0 12px; }.group { padding: 20px; border: 1px solid var(--color-border); border-radius: 8px; margin: 16px 0; }.review-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 24px; }table { width: 100%; min-width: 620px; border-collapse: collapse; margin: 20px 0; }th, td { text-align: left; padding: 12px; border-bottom: 1px solid var(--color-border); overflow-wrap: anywhere; }th { color: var(--color-muted); font-weight: 500; }.footer-actions { border-top: 1px solid var(--color-border); padding-top: 24px; margin-top: 28px; }
@media (max-width: 760px) { .step-panel :deep(.n-steps) { display: none; }.available-routes, .review-grid { grid-template-columns: 1fr; } }
</style>
