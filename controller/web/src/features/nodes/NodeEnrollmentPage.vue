<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import PageHeader from '../../components/PageHeader.vue'
import { pageLocation } from '../../app/router'
import { useWorkspace } from '../../demo/workspace'
import EnrollmentWizard from './components/EnrollmentWizard.vue'

const route = useRoute(), router = useRouter()
const { workspace } = useWorkspace()
const { t } = useI18n()
const freshId = ref(`node-${crypto.randomUUID()}`)
const draftId = computed(() => typeof route.query.draftId === 'string' ? route.query.draftId : freshId.value)
const missing = computed(() => draftId.value !== freshId.value && !workspace.drafts.some(draft => draft.id === draftId.value && draft.feature === 'nodes'))
watch(() => route.query.draftId, id => {
  if (typeof id !== 'string') {
    freshId.value = `node-${crypto.randomUUID()}`
    void router.replace(pageLocation('node-new', {}, { draftId: freshId.value, step: route.query.step || 'basics' }))
  }
}, { immediate: true })
</script>

<template>
  <div>
    <PageHeader :title="t('features.nodes.NodeEnrollmentPage.title')" :description="t('features.nodes.NodeEnrollmentPage.goal')" />
    <section v-if="missing" class="panel">
      <h2>{{ t('features.nodes.wizard.draftMissingTitle') }}</h2>
      <p>{{ t('features.nodes.wizard.draftMissingBody') }}</p>
      <div class="link-list"><RouterLink :to="pageLocation('nodes')">{{ t('features.nodes.wizard.backList') }}</RouterLink><RouterLink :to="pageLocation('node-new', {}, { step: 'basics' })">{{ t('features.nodes.wizard.startNew') }}</RouterLink></div>
    </section>
    <EnrollmentWizard v-else :key="draftId" :draft-id="draftId" />
  </div>
</template>
