<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NInput, NModal, NPagination, NSelect, useDialog, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { pageLocation, entityLocation } from '../../app/router'
import { useWorkspace } from '../../demo/workspace'
import { useFormat } from '../../composables/useFormat'
import type { RuleProfile } from '../../domain'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import RuleReview from './RuleReview.vue'
import { analyzeRules, newId, profileReferences, profileSource } from './rules'
const { t } = useI18n(); const tr = (key: string) => t(`features.rule-profiles.${key}`)
const { workspace, commands } = useWorkspace(); const route = useRoute(); const router = useRouter(); const format = useFormat(); const message = useMessage(); const dialog = useDialog()
const q = computed({ get: () => typeof route.query.q === 'string' ? route.query.q : '', set: (value: string) => updateQuery({q:value || undefined,page:'1'}) })
const sort = computed({ get: () => typeof route.query.sort === 'string' ? route.query.sort : '-updatedAt', set: (value: string) => updateQuery({sort:value,page:'1'}) })
const pageSize = computed(() => Math.min(100, Number(route.query.pageSize) || 10))
const filtered = computed(() => {
  const text = q.value.toLowerCase().trim()
  return workspace.profiles.filter(profile => `${profile.name} ${profile.description ?? ''}`.toLowerCase().includes(text)).slice().sort((a,b) => {
    const order = sort.value.includes('name') ? a.name.localeCompare(b.name) : a.updatedAt.localeCompare(b.updatedAt)
    return (sort.value.startsWith('-') ? -order : order) || a.id.localeCompare(b.id)
  })
})
const page = computed(() => Math.max(1, Math.min(Number(route.query.page) || 1, Math.ceil(filtered.value.length / pageSize.value) || 1)))
const rows = computed(() => filtered.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
function updateQuery(patch: Record<string, string | undefined>) { void router.replace(pageLocation('rule-profiles', {}, {...route.query,...patch})) }
const previewId = ref<string>(), context = ref<string | null>(null), deleteId = ref<string>(), failed = ref(false)
const preview = computed(() => workspace.profiles.find(profile => profile.id === previewId.value))
const deleting = computed(() => workspace.profiles.find(profile => profile.id === deleteId.value))
const drafts = computed(() => workspace.drafts.filter(draft => draft.feature === 'rule-profiles'))
const subscriptionDraftReferences = computed(() => workspace.drafts.filter(draft => draft.feature === 'subscriptions' && draft.profileId === deleteId.value))
const references = computed(() => deleteId.value ? profileReferences(workspace, deleteId.value) : [])
const previewResult = computed(() => preview.value ? analyzeRules(workspace, profileSource(preview.value), preview.value.rules.find(rule => rule.type === 'MATCH')?.target ?? 'All-Proxies', preview.value.domainSetIds, context.value ?? undefined) : undefined)
function duplicate(id: string) {
  const profile = workspace.profiles.find(profile => profile.id === id)
  if (!profile) return
  if (workspace.scenario === 'error') { failed.value = true; message.error(tr('failed')); return }
  const copy: RuleProfile = JSON.parse(JSON.stringify(profile)); copy.id = newId('profile'); copy.revision = 1; copy.updatedAt = new Date().toISOString()
  let index = 1; let candidate = `${profile.name.slice(0,85)} (Copy)`
  while (workspace.profiles.some(item => item.name.toLowerCase() === candidate.toLowerCase())) { index++; candidate = `${profile.name.slice(0,80)} (Copy ${index})` }
  copy.name = candidate; copy.rules = copy.rules.map((rule,i) => ({...rule,id:`${copy.id}-${i + 1}`})); commands.upsert('profiles', copy); failed.value = false; message.success(tr('duplicated'))
}
function removeDraft(id: string) { dialog.warning({title:tr('deleteDraft'),content:tr('deleteDraftBody'),positiveText:tr('delete'),negativeText:tr('cancel'),onPositiveClick:() => { commands.remove('drafts',id) }}) }
function remove() {
  if (!deleting.value || references.value.length || subscriptionDraftReferences.value.length) return
  if (workspace.scenario === 'error') { failed.value = true; message.error(tr('failed')); return }
  const id = deleting.value.id
  commands.remove('profiles', id)
  workspace.drafts.filter(draft => draft.feature === 'rule-profiles' && draft.objectId === id).forEach(draft => commands.remove('drafts', draft.id))
  deleteId.value = undefined; failed.value = false; message.success(tr('deleted'))
}
</script>
<template>
  <div class="profile-list">
    <PageHeader :title="tr('RuleProfilesPage.title')" :description="tr('RuleProfilesPage.goal')"><template #actions><NButton type="primary" @click="router.push(pageLocation('profile-new'))">{{ tr('new') }}</NButton></template></PageHeader>
    <nav class="subnav" :aria-label="tr('profiles')"><RouterLink :to="pageLocation('subscriptions')">{{ tr('subscriptions') }}</RouterLink><RouterLink :to="pageLocation('rule-profiles')" aria-current="page">{{ tr('profiles') }}</RouterLink></nav>
    <NAlert type="info" :bordered="false">{{ tr('offline') }}</NAlert><NAlert v-if="workspace.scenario === 'stale'" type="warning">{{ tr('stale') }}</NAlert><NAlert v-if="failed || workspace.scenario === 'error'" type="error">{{ tr('failed') }}</NAlert>
    <section class="panel"><div class="toolbar"><NInput v-model:value="q" clearable :placeholder="tr('search')" :input-props="{'aria-label':tr('search')}" :maxlength="120" /><NSelect v-model:value="sort" :aria-label="tr('sort')" :options="[{label:tr('nameAsc'),value:'name'},{label:tr('nameDesc'),value:'-name'},{label:tr('newest'),value:'-updatedAt'},{label:tr('oldest'),value:'updatedAt'}]" /></div>
      <div v-if="rows.length" class="table-scroll"><table><thead><tr><th>{{ tr('name') }}</th><th>{{ tr('description') }}</th><th>{{ tr('updated') }}</th><th>{{ tr('references') }}</th><th>{{ tr('actions') }}</th></tr></thead><tbody><tr v-for="profile in rows" :key="profile.id"><td><RouterLink :to="entityLocation('profile', profile.id)"><strong>{{ profile.name }}</strong></RouterLink><small>{{ tr('revision') }} {{ profile.revision }}</small></td><td>{{ profile.description || '—' }}</td><td>{{ format.date(profile.updatedAt) }}</td><td><details v-if="profileReferences(workspace, profile.id).length"><summary>{{ profileReferences(workspace, profile.id).length }}</summary><div class="reference-links"><RouterLink v-for="sub in profileReferences(workspace, profile.id)" :key="sub.id" :to="entityLocation('subscription',sub.id)">{{ sub.name }}</RouterLink></div></details><span v-else>0</span></td><td><div class="row-actions"><NButton size="small" @click="router.push(entityLocation('profile',profile.id))">{{ tr('edit') }}</NButton><NButton size="small" @click="duplicate(profile.id)">{{ tr('duplicate') }}</NButton><NButton size="small" @click="previewId = profile.id; context = null">{{ tr('preview') }}</NButton><NButton size="small" @click="deleteId = profile.id">{{ tr('delete') }}</NButton></div></td></tr></tbody></table></div>
      <EmptyState v-else :title="tr(q ? 'filtered' : 'empty')" :description="tr('emptyBody')" :filtered="!!q" :action-label="tr('new')" @clear="q = ''" @action="router.push(pageLocation('profile-new'))" />
      <NPagination v-if="filtered.length > pageSize" :page="page" :page-size="pageSize" :item-count="filtered.length" :page-slot="5" class="pagination" @update:page="updateQuery({page:String($event)})" />
    </section>
    <section v-if="drafts.length" class="panel"><h2>{{ tr('drafts') }}</h2><div class="drafts"><article v-for="draft in drafts" :key="draft.id"><div><strong>{{ draft.name || tr('new') }}</strong><small>{{ format.date(draft.updatedAt) }}</small></div><div class="actions"><NButton @click="router.push(pageLocation(draft.objectId ? 'profile-edit' : 'profile-new',draft.objectId ? {profileId:draft.objectId} : {},{draftId:draft.id}))">{{ tr('resume') }}</NButton><NButton @click="removeDraft(draft.id)">{{ tr('delete') }}</NButton></div></article></div></section>
    <NModal :show="!!previewId" preset="card" :title="`${tr('preview')}: ${preview?.name ?? tr('missing')}`" style="width:min(1040px,calc(100vw - 32px));max-height:90vh;overflow:auto" @update:show="!$event && (previewId = undefined)">
      <template v-if="preview && previewResult"><label class="context-label">{{ tr('context') }}<NSelect v-model:value="context" clearable :placeholder="tr('noContext')" :options="workspace.subscriptions.map(sub => ({label:sub.name,value:sub.id}))" /></label><p>{{ tr(context ? 'contextSelected' : 'contextHint') }}</p><RuleReview :result="previewResult" :selected-sets="[...preview.domainSetIds]" readonly @focus="router.push(entityLocation('profile',preview.id))" /><div class="actions"><NButton @click="previewId = undefined">{{ tr('close') }}</NButton><NButton type="primary" @click="router.push(pageLocation('profile-edit',{profileId:preview.id},{subscriptionId:context ?? undefined}))">{{ tr('edit') }}</NButton></div></template><p v-else>{{ tr('missingBody') }}</p>
    </NModal>
    <NModal :show="!!deleteId" preset="card" :title="tr('deleteTitle')" style="width:min(560px,calc(100vw - 32px))" @update:show="!$event && (deleteId = undefined)">
      <template v-if="deleting"><p>{{ deleting.name }}</p><NAlert v-if="references.length || subscriptionDraftReferences.length" type="warning">{{ tr('blockedDelete') }}</NAlert><p v-else>{{ tr('deleteBody') }}</p><ul><li v-for="sub in references" :key="sub.id"><RouterLink :to="entityLocation('subscription',sub.id)">{{ sub.name }}</RouterLink> · <RouterLink :to="pageLocation('subscription-edit',{subscriptionId:sub.id},{step:'policy'})">{{ tr('replace') }}</RouterLink></li><li v-for="draft in subscriptionDraftReferences" :key="draft.id"><RouterLink :to="pageLocation(draft.objectId ? 'subscription-edit' : 'subscription-new', draft.objectId ? {subscriptionId:draft.objectId} : {}, {draftId:draft.id,step:'policy'})">{{ tr('saveDraft') }}: {{ draft.name }}</RouterLink></li></ul><div class="actions"><NButton @click="deleteId = undefined">{{ tr('cancel') }}</NButton><NButton type="error" :disabled="!!references.length || !!subscriptionDraftReferences.length" @click="remove">{{ tr('delete') }}</NButton></div></template><p v-else>{{ tr('missingBody') }}</p>
    </NModal>
  </div>
</template>
<style scoped>
.profile-list{display:grid;gap:20px}.subnav{display:flex;gap:24px;border-bottom:1px solid var(--color-border)}.subnav a{padding:10px 0}.subnav [aria-current]{border-bottom:2px solid var(--color-primary);font-weight:600}.toolbar{display:grid;grid-template-columns:minmax(180px,1fr) 220px;gap:16px;margin-bottom:18px}table{width:100%;border-collapse:collapse;font-size:14px}th,td{text-align:left;padding:14px 12px;border-bottom:1px solid var(--color-border);vertical-align:top}th{font-size:12px;color:var(--color-muted)}td:nth-child(2){max-width:260px;overflow-wrap:anywhere}small{display:block;color:var(--color-muted);margin-top:5px}.row-actions{display:flex;gap:6px;flex-wrap:wrap;min-width:230px}.reference-links{display:grid;gap:8px;padding-top:10px}.drafts{display:grid;gap:12px}.drafts article{display:flex;justify-content:space-between;align-items:center;gap:16px;flex-wrap:wrap;padding:12px 0;border-bottom:1px solid var(--color-border)}.pagination{margin-top:20px}.context-label{display:grid;gap:8px;max-width:480px}summary{cursor:pointer;min-height:32px}@media(max-width:600px){.toolbar{grid-template-columns:1fr}.row-actions{min-width:140px}}
@media(max-width:760px){:deep(.n-button){min-height:44px}}
</style>
