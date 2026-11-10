<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspace } from '../../demo/workspace'
import EntityLink from '../../components/EntityLink.vue'
import RoutePath from '../../components/RoutePath.vue'
import { nodeName, pathNodes, type RevisionSnapshot } from './route-model'
const props = defineProps<{ revision: RevisionSnapshot }>()
const { workspace } = useWorkspace(); const { t } = useI18n()
const entry = computed(() => workspace.listeners.find(listener => listener.id === props.revision.entryListenerId))
const segments = computed(() => props.revision.links.map(link => ({ link, listener: workspace.listeners.find(listener => listener.id === link.listenerId), tunnel: workspace.tunnels.find(tunnel => tunnel.id === link.tunnelId) })))
</script>
<template>
  <div class="topology">
    <p class="caption">{{ t('features.routes.fixedPath') }}</p>
    <RoutePath :nodes="pathNodes(workspace, revision)" />
    <p v-if="!revision.nodeIds.length" role="alert">{{ t('features.routes.invalidPath') }}</p>
    <div class="hops">
      <div class="hop">
        <strong>{{ t('features.routes.entry') }}</strong>
        <EntityLink v-if="revision.nodeIds[0]" kind="node" :id="revision.nodeIds[0]" :label="nodeName(workspace, revision.nodeIds[0])" />
        <span v-if="entry" class="mono">{{ t(`features.routes.protocols.${entry.protocol}`) }} · {{ entry.address }}:{{ entry.port }} · {{ entry.transport }}</span>
        <span v-else role="alert">{{ t('features.routes.missingResource', { id: revision.entryListenerId }) }}</span>
      </div>
      <div v-for="({ link, listener, tunnel }, index) in segments" :key="`${index}-${link.listenerId}`" class="hop">
        <strong>{{ t('features.routes.internalHop', { index: index + 1 }) }}</strong>
        <span><EntityLink kind="node" :id="link.fromNodeId" :label="nodeName(workspace, link.fromNodeId)" /> → <EntityLink kind="node" :id="link.toNodeId" :label="nodeName(workspace, link.toNodeId)" /></span>
        <span class="mono">{{ t(`features.routes.protocols.${link.protocol}`) }} · {{ listener ? `${listener.address}:${listener.port} · ${listener.transport}` : t('features.routes.missingResource', { id: link.listenerId }) }}</span>
        <span v-if="link.tunnelId" class="mono">{{ t('features.routes.tunnel') }}: {{ tunnel ? `${tunnel.name} · TCP + mTLS · 127.0.0.1:${tunnel.mappingPort} · r${link.tunnelRevision ?? tunnel.revision}` : t('features.routes.missingResource', { id: link.tunnelId }) }}</span>
      </div>
    </div>
    <p class="muted">{{ t('features.routes.portHint') }}</p>
  </div>
</template>
<style scoped>
.caption { font-weight: 600; margin-top: 0; }.hops { display: grid; gap: 12px; margin: 16px 0; }.hop { display: grid; gap: 6px; padding: 14px 16px; border: 1px solid var(--color-border); border-left: 3px solid #5b9478; border-radius: 8px; background: #f8fbf9; overflow-wrap: anywhere; }.mono { font-family: ui-monospace, monospace; }.muted { color: #61706b; font-size: 13px; }
</style>
