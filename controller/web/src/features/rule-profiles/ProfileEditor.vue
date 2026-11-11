<script setup lang="ts">
import { computed, h, nextTick, ref, watch } from 'vue'
import { onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NFormItem, NInput, NSelect, useDialog, useMessage, type InputInst } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { Draft, RuleProfile } from '../../domain'
import { useWorkspace } from '../../demo/workspace'
import { useDraft } from '../../composables/useDraft'
import { pageLocation, safeReturnTo } from '../../app/router'
import PageHeader from '../../components/PageHeader.vue'
import RuleReview from './RuleReview.vue'
import { analyzeRules, containsSensitiveInput, isDomain, newId, normalizeMatchText, parseRules, profileSource } from './rules'
const props = defineProps<{ profileId?: string }>()
const { workspace, commands } = useWorkspace(); const route = useRoute(); const router = useRouter(); const message = useMessage(); const dialog = useDialog()
const { t } = useI18n(); const tr = (key: string) => t(`features.rule-profiles.${key}`)
const source = workspace.profiles.find(profile => profile.id === props.profileId)
const initialRevision = ref(source?.revision)
const suppliedDraft = typeof route.query.draftId === 'string' ? route.query.draftId : undefined
const suppliedRecord = workspace.drafts.find(draft => draft.id === suppliedDraft)
// A subscription may hand over its own draft ID. Keep that draft intact and use a separate profile editor instance.
const id = suppliedRecord && suppliedRecord.feature !== 'rule-profiles' && suppliedRecord.data?.subscription ? `profile-editor-${suppliedDraft}${props.profileId ? `-${props.profileId}` : ''}` : suppliedDraft ?? (props.profileId ? `profile-editor-${props.profileId}` : newId('profile-draft'))
const existingDraft = workspace.drafts.find(draft => draft.id === id)
const draftMismatch = computed(() => !!existingDraft && (existingDraft.feature !== 'rule-profiles' || existingDraft.objectId !== props.profileId))
const initial: Draft = { id, feature: 'rule-profiles', objectId: props.profileId, profileId: props.profileId, name: source?.name ?? '', nodeIds: [], routeIds: [], updatedAt: new Date().toISOString(), data: { profile: { description: source?.description ?? '', text: source ? profileSource(source) : '', fallback: source?.rules.find(rule => rule.type === 'MATCH')?.target ?? 'All-Proxies', domainSetIds: [...(source?.domainSetIds ?? [])] } } }
const draft = useDraft(id, initial)
// Old drafts without form data receive explicit model defaults.
function ensureForm() { if (!draft.value.value.data?.profile) draft.value.value.data = { ...draft.value.value.data, profile: JSON.parse(JSON.stringify(initial.data!.profile)) } }
ensureForm()
const form = computed(() => draft.value.value.data?.profile ?? initial.data!.profile!)
function discardChanges() { draft.discard(); ensureForm(); chosenMatch.value = undefined }
const chosenMatch = ref<number>(); const helperTarget = ref('REJECT'), helperDomain = ref('example.invalid'), helperType = ref('DOMAIN-SUFFIX')
const selectedSubscription = computed({ get: () => typeof route.query.subscriptionId === 'string' ? route.query.subscriptionId : null, set: (value: string | null) => { void router.replace(pageLocation(props.profileId ? 'profile-edit' : 'profile-new', props.profileId ? { profileId: props.profileId } : {}, { ...route.query, subscriptionId: value ?? undefined })) } })
const result = computed(() => analyzeRules(workspace, form.value.text, form.value.fallback, form.value.domainSetIds, selectedSubscription.value ?? undefined, chosenMatch.value))
const review = ref<InstanceType<typeof RuleReview> | null>(null)
const tried = ref(false), operationFailed = ref(false)
const input = ref<InputInst | null>(null)
const returnPath = computed(() => safeReturnTo(route.query.returnTo))
const profileConflict = computed(() => props.profileId && workspace.profiles.find(p => p.id === props.profileId)?.revision !== initialRevision.value)
const nameError = computed(() => !draft.value.value.name.trim() || draft.value.value.name.trim().length > 100 || containsSensitiveInput(draft.value.value.name) || workspace.profiles.some(p => p.id !== props.profileId && p.name.toLowerCase() === draft.value.value.name.trim().toLowerCase()))
const descriptionError = computed(() => form.value.description.length > 500)
onBeforeRouteUpdate((to, from) => {
  if (!draft.dirty.value || (to.params.profileId === from.params.profileId && to.query.draftId === from.query.draftId)) return true
  return new Promise<boolean>(resolve => {
    let settled = false
    const finish = (value: boolean) => { if (!settled) { settled = true; resolve(value) } }
    const modal = dialog.warning({ title: t('common.dirty'), content: t('common.dirtyBody'), action: () => h('div', {class:'actions'}, [
      h(NButton, {onClick: () => { finish(false); modal.destroy() }}, {default: () => t('common.stay')}),
      h(NButton, {onClick: () => { draft.discard(); finish(true); modal.destroy() }}, {default: () => t('common.discard')}),
      h(NButton, {onClick: () => { draft.save(); finish(true); modal.destroy() }}, {default: () => tr('saveDraft')}),
    ]), onClose: () => finish(false), onMaskClick: () => finish(false), onAfterLeave: () => finish(false) })
  })
})
const textLimit = computed(() => form.value.text.length > 100000 || form.value.text.split('\n').length > 2000)
watch(() => form.value.text, () => { chosenMatch.value = undefined; const matches = parseRules(form.value.text).lines.filter(line => line.type === 'MATCH'); if (matches.length === 1) form.value.fallback = matches[0]!.target }, { flush: 'sync' })
function updateText(text: string) {
  if (containsSensitiveInput(text)) { message.error(tr('sensitive')); return }
  if (text.length > 100000 || text.split('\n').length > 2000) { message.error(tr('errors.inputLimit')); return }
  form.value.text = text
}
function updateDescription(text: string) { if (containsSensitiveInput(text)) message.error(tr('sensitive')); else form.value.description = text }
function updateName(text: string) { if (containsSensitiveInput(text)) message.error(tr('sensitive')); else draft.value.value.name = text }
async function focusLine(line: number) {
  await nextTick(); const element = input.value?.textareaElRef
  if (!element) return
  const lines = form.value.text.split('\n'), start = lines.slice(0, line - 1).reduce((sum, value) => sum + value.length + 1, 0)
  element.focus(); element.setSelectionRange(start, start + (lines[line - 1]?.length ?? 0)); element.scrollTop = Math.max(0, (line - 3) * 22)
}
async function insertTarget() {
  const element = input.value?.textareaElRef; const text = form.value.text; const start = element?.selectionStart ?? text.length, end = element?.selectionEnd ?? text.length
  updateText(text.slice(0, start) + helperTarget.value + text.slice(end)); await nextTick(); element?.focus(); element?.setSelectionRange(start + helperTarget.value.length, start + helperTarget.value.length)
}
function appendRule() {
  if (!isDomain(helperDomain.value)) { message.error(t('features.rule-profiles.errors.value', {detail:helperDomain.value})); return }
  updateText(`${form.value.text}${form.value.text && !form.value.text.endsWith('\n') ? '\n' : ''}${helperType.value},${helperDomain.value.toLowerCase()},${helperTarget.value}`)
}
function chooseMatch(line: number, target: string) { chosenMatch.value = line; form.value.fallback = target }
async function saveDraft() {
  if (draftMismatch.value || containsSensitiveInput(form.value.text) || containsSensitiveInput(form.value.description) || containsSensitiveInput(draft.value.value.name)) { message.error(tr('sensitive')); return }
  ensureForm(); draft.save(); message.success(tr('draftSaved'))
  if (!suppliedDraft) await router.replace(pageLocation(props.profileId ? 'profile-edit' : 'profile-new', props.profileId ? { profileId: props.profileId } : {}, { ...route.query, draftId: id }))
}
async function navigateBack(profileId?: string) {
  const back = safeReturnTo(returnPath.value)
  if (back) {
    const resolved = router.resolve(back)
    if (['subscription-new', 'subscription-edit'].includes(String(resolved.name))) {
      if (resolved.name === 'subscription-edit' && !workspace.subscriptions.some(sub => sub.id === resolved.params.subscriptionId)) { message.error(t('features.rule-profiles.errors.subscriptionMissing', {detail:String(resolved.params.subscriptionId)})); await router.push(pageLocation('rule-profiles')); return }
      await router.push(pageLocation(resolved.name === 'subscription-new' ? 'subscription-new' : 'subscription-edit', resolved.name === 'subscription-edit' ? { subscriptionId: String(resolved.params.subscriptionId) } : {}, { ...resolved.query, ...(profileId ? { profileId } : {}) })); return
    }
  }
  await router.push(pageLocation('rule-profiles'))
}
async function saveProfile() {
  tried.value = true
  if (!result.value.valid) review.value?.showValidation()
  if (draftMismatch.value || nameError.value || descriptionError.value || textLimit.value || !result.value.valid || profileConflict.value || containsSensitiveInput(form.value.text)) return
  if (workspace.scenario === 'error') { operationFailed.value = true; message.error(tr('failed')); return }
  const profileId = props.profileId ?? newId('profile')
  const normalized = normalizeMatchText(form.value.text, result.value.finalTarget)
  const profile: RuleProfile = { id: profileId, name: draft.value.value.name.trim(), description: form.value.description.trim(), revision: (initialRevision.value ?? 0) + 1, updatedAt: new Date().toISOString(), rules: result.value.generated.map(rule => ({ ...rule, options: rule.options ? [...rule.options] : undefined })), domainSetIds: [...form.value.domainSetIds], sourceText: normalized }
  commands.upsert('profiles', profile)
  initialRevision.value = profile.revision
  form.value.text = normalized
  draft.value.value.name = profile.name
  form.value.description = profile.description ?? ''
  draft.save() // settle the shared dirty guard, then remove the completed editor draft
  commands.remove('drafts', id)
  message.success(tr('saved'))
  if (returnPath.value) await navigateBack(profileId)
  else await router.replace(pageLocation('profile-edit', { profileId }, { subscriptionId: selectedSubscription.value ?? undefined }))
}
function setIds(ids: string[]) { form.value.domainSetIds = ids }
function fallbackChanged(value: string) {
  form.value.fallback = value
  // A single pasted MATCH is authoritative. Edit its target in the original text.
  const matches = result.value.matches
  if (matches.length === 1) {
    const lines = form.value.text.split('\n'), at = matches[0]!.line - 1
    lines[at] = `MATCH,${value}`; updateText(lines.join('\n'))
  } else if (matches.length > 1) chosenMatch.value = undefined
}
</script>
<template>
  <div class="editor">
    <PageHeader :title="source ? `${tr('edit')}: ${source.name}` : tr('new')" :description="tr('RuleProfileEditorPage.goal')"><template #actions><NButton @click="navigateBack()">{{ tr(returnPath ? 'return' : 'back') }}</NButton></template></PageHeader>
    <NAlert type="info" :bordered="false">{{ tr('offline') }}</NAlert>
    <NAlert v-if="draftMismatch" type="error">{{ tr('errors.draftMismatch') }}</NAlert>
    <NAlert v-if="profileConflict" type="error">{{ tr('errors.conflict') }}</NAlert>
    <NAlert v-if="workspace.scenario === 'stale'" type="warning">{{ tr('stale') }}</NAlert>
    <NAlert v-if="operationFailed" type="error">{{ tr('failed') }} <NButton text @click="saveProfile">{{ tr('retry') }}</NButton></NAlert>
    <form v-if="!draftMismatch" @submit.prevent="saveProfile">
      <section class="panel basics"><NFormItem :label="tr('name')" :validation-status="tried && nameError ? 'error' : undefined" :feedback="tried && nameError ? tr('errors.name') : undefined"><NInput :value="draft.value.value.name" :input-props="{'aria-label':tr('name')}" :maxlength="100" @update:value="updateName" /></NFormItem><NFormItem :label="tr('description')"><NInput :value="form.description" :input-props="{'aria-label':tr('description')}" :maxlength="500" type="textarea" :autosize="{minRows:2,maxRows:4}" @update:value="updateDescription" /></NFormItem></section>
      <section class="panel context"><NFormItem :label="tr('context')"><NSelect v-model:value="selectedSubscription" clearable :placeholder="tr('noContext')" :options="workspace.subscriptions.map(sub => ({label:sub.name,value:sub.id}))" /></NFormItem><p>{{ tr(selectedSubscription ? 'contextSelected' : 'contextHint') }}</p><p v-if="returnPath">{{ tr('contextDraft') }}</p></section>
      <div class="input-grid"><section class="panel rules-panel"><h2>{{ tr('rules') }}</h2><p id="rule-syntax">{{ tr('syntax') }}</p><NInput ref="input" :value="form.text" type="textarea" :autosize="{minRows:16,maxRows:28}" :input-props="{ 'aria-label': tr('rules'), 'aria-describedby': 'rule-syntax', spellcheck: false }" @update:value="updateText" />
        <NAlert v-if="result.matches.length > 1" type="warning" class="match-picker"><p>{{ tr('matchChoice') }}</p><div class="actions"><NButton v-for="match in result.matches" :key="match.line" :type="chosenMatch === match.line ? 'primary' : 'default'" @click="chooseMatch(match.line, match.target)">{{ t('features.rule-profiles.chooseMatch', {line:match.line,target:match.target}) }}</NButton></div></NAlert>
        <NFormItem :label="tr('fallback')" class="fallback"><NSelect :value="result.finalTarget" filterable :options="result.catalog.rows.map(row => ({label:row.name,value:row.name}))" @update:value="fallbackChanged" /></NFormItem>
      </section><aside class="panel helper"><h2>{{ tr('targets') }}</h2><p>{{ tr('targetHint') }}</p><NFormItem :label="tr('target')"><NSelect v-model:value="helperTarget" filterable :options="result.catalog.rows.map(row => ({label:row.name,value:row.name}))" /></NFormItem><NButton block @click="insertTarget">{{ tr('insertTarget') }}</NButton><div class="target-links"><div v-for="row in result.catalog.rows.filter(row => row.routeId)" :key="row.name"><code>{{ row.name }}</code><RouterLink :to="pageLocation('route-detail', {routeId:row.routeId!})">{{ row.routeName }}</RouterLink></div></div><NFormItem :label="tr('helperDomain')"><NInput v-model:value="helperDomain" :input-props="{'aria-label':tr('helperDomain')}" /></NFormItem><NFormItem :label="tr('helperType')"><NSelect v-model:value="helperType" :options="['DOMAIN','DOMAIN-SUFFIX'].map(value => ({label:value,value}))" /></NFormItem><NButton block @click="appendRule">{{ tr('insertRule') }}</NButton></aside></div>
      <section class="panel"><div class="review-status" role="status">{{ result.valid ? tr('valid') : t('features.rule-profiles.errorCount', {count:result.issues.filter(issue => !issue.warning).length}) }}</div><RuleReview ref="review" :result="result" :selected-sets="form.domainSetIds" @focus="focusLine" @update:selected-sets="setIds" /></section>
      <NAlert v-if="tried && (descriptionError || textLimit)" type="error">{{ tr(descriptionError ? 'errors.description' : 'errors.inputLimit') }}</NAlert>
      <footer class="panel footer"><span>{{ draft.dirty.value ? t('common.dirty') : tr('saveDraft') }}</span><div class="actions"><NButton @click="discardChanges">{{ tr('discard') }}</NButton><NButton @click="navigateBack()">{{ tr('cancel') }}</NButton><NButton @click="saveDraft">{{ tr('saveDraft') }}</NButton><NButton type="primary" attr-type="submit" :disabled="!!draftMismatch || !!profileConflict">{{ tr('save') }}</NButton></div></footer>
    </form>
  </div>
</template>
<style scoped>
.editor, form { grid-template-columns: minmax(0, 1fr); min-width: 0; }
.editor > *, form > *, .basics > *, .helper > * { min-width: 0; }
.editor,form{display:grid;gap:20px}.basics{display:grid;grid-template-columns:1fr 2fr;gap:24px}.input-grid{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:20px}.input-grid h2{font-size:17px;margin-top:0}.rules-panel,.helper{min-width:0}.rules-panel :deep(textarea){font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:13px;line-height:22px}.context p,.helper p,#rule-syntax{color:var(--color-muted);font-size:13px}.context :deep(.n-form-item){max-width:480px}.fallback,.match-picker{margin-top:20px}.target-links{display:grid;gap:14px;margin:20px 0}.target-links code,.target-links a{display:block;overflow-wrap:anywhere;font-size:12px}.footer{display:flex;align-items:center;justify-content:space-between;gap:20px;position:sticky;bottom:8px;box-shadow:0 4px 24px #163f1820;z-index:2}.footer span{font-size:13px;color:var(--color-muted)}.review-status{font-size:13px;color:var(--color-muted);margin-bottom:12px}@media(max-width:1000px){.input-grid{grid-template-columns:minmax(0,1fr)}.helper{display:grid;grid-template-columns:1fr 1fr;gap:12px}.helper h2,.helper p,.target-links{grid-column:1/-1}}@media(max-width:600px){.basics,.helper{grid-template-columns:1fr}.footer{position:static;align-items:flex-start;flex-direction:column}.footer .actions{width:100%;display:grid;grid-template-columns:1fr 1fr}}
@media(max-width:760px){:deep(.n-button){min-height:44px}}
</style>
