<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { entityLocation } from '../../app/router'
import { useWorkspace } from '../../demo/workspace'
import { sourceName, type Source } from './activity-model'
const props = defineProps<{ source: Source }>()
const { workspace } = useWorkspace()
const { t } = useI18n()
const name = computed(() => sourceName(workspace, props.source))
</script>
<template>
  <RouterLink :to="entityLocation(source.kind, source.id)">{{ name ?? source.id }}</RouterLink>
  <small v-if="!name" class="missing"> · {{ t('features.activity.missingSource') }}</small>
</template>
<style scoped>.missing { color: #9a5920; }</style>
