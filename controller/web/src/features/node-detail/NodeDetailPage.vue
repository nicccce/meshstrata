<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useWorkspace, resetEpoch } from '../../demo/workspace'
import { pageLocation } from '../../app/router'
import NodeDetailContent from './NodeDetailContent.vue'
const route = useRoute()
const { t } = useI18n()
const { workspace } = useWorkspace()
const node = computed(() => workspace.nodes.find(item => item.id === route.params.nodeId))
</script>

<template>
  <NodeDetailContent v-if="node" :key="`${node.id}-${resetEpoch}`" :node-id="node.id" />
  <section v-else class="panel" role="status">
    <h1>{{ t('common.notFound') }}</h1>
    <p>{{ t('common.missing') }}</p>
    <RouterLink :to="pageLocation('nodes')">{{ t('common.back') }}</RouterLink>
  </section>
</template>
