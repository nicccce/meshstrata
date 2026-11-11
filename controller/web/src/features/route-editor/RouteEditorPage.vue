<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton } from 'naive-ui'
import { useWorkspace } from '../../demo/workspace'
import { pageLocation } from '../../app/router'
import RouteWizard from './RouteWizard.vue'
const route = useRoute(); const { t } = useI18n(); const { workspace } = useWorkspace()
const routeId = computed(() => typeof route.params.routeId === 'string' ? route.params.routeId : undefined)
const draftId = computed(() => typeof route.query.draftId === 'string' ? route.query.draftId : undefined)
const missing = computed(() => (routeId.value && !workspace.routes.some(r => r.id === routeId.value)) || (draftId.value && !workspace.drafts.some(d => d.id === draftId.value && ['route', 'routes', 'route-editor'].includes(d.feature) && (!routeId.value || !d.objectId || d.objectId === routeId.value))))
</script>
<template>
  <NAlert v-if="missing" type="error" :title="t('features.route-editor.notFound')">
    <p>{{ t('features.route-editor.notFoundBody') }}</p>
    <RouterLink :to="pageLocation('routes')"><NButton>{{ t('features.route-editor.backToRoutes') }}</NButton></RouterLink>
  </NAlert>
  <RouteWizard v-else :key="`${routeId ?? 'new'}:${draftId ?? 'initial'}`" :route-id="routeId" :draft-id="draftId" />
</template>
