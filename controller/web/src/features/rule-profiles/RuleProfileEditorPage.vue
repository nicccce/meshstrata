<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useWorkspace, resetEpoch } from '../../demo/workspace'
import { pageLocation } from '../../app/router'
import ProfileEditor from './ProfileEditor.vue'
const route = useRoute(); const { workspace } = useWorkspace(); const { t } = useI18n()
const profileId = computed(() => typeof route.params.profileId === 'string' ? route.params.profileId : undefined)
const missing = computed(() => !!profileId.value && !workspace.profiles.some(profile => profile.id === profileId.value))
const key = computed(() => `${profileId.value ?? 'new'}:${String(route.query.draftId ?? '')}:${resetEpoch.value}`)
</script>
<template>
  <section v-if="missing" class="panel"><h1>{{ t('features.rule-profiles.missing') }}</h1><p>{{ t('features.rule-profiles.missingBody') }}</p><RouterLink :to="pageLocation('rule-profiles')">{{ t('features.rule-profiles.back') }}</RouterLink></section>
  <ProfileEditor v-else :key="key" :profile-id="profileId" />
</template>
