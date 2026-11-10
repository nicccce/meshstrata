<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspace } from '../../demo/workspace'
import EntityLink from '../../components/EntityLink.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import EmptyState from '../../components/EmptyState.vue'
import { NAlert } from 'naive-ui'
import { pageLocation } from '../../app/router'
import { nodeName, referencedSubscriptions, routeResources, type RouteSnapshot } from './route-model'
const props = defineProps<{ route: RouteSnapshot }>()
const { workspace } = useWorkspace(); const { t } = useI18n(); const prefix = 'features.routes.'
const resources = computed(() => routeResources(workspace, props.route))
const subscriptions = computed(() => referencedSubscriptions(workspace, props.route.id))
const entries = computed(() => workspace.entries.filter(entry => entry.routeId === props.route.id))
</script>
<template>
  <div class="resources">
    <NAlert type="info" :bordered="false">{{ t(`${prefix}resourceHint`) }}</NAlert>
    <NAlert v-if="resources.missingListeners.length || resources.missingTunnels.length" type="error">{{ t(`${prefix}missingResource`, { id: [...resources.missingListeners, ...resources.missingTunnels].join(', ') }) }}</NAlert>
    <section class="panel"><h2>{{ t(`${prefix}listeners`) }}</h2>
      <EmptyState v-if="!resources.listeners.length" :title="t(`${prefix}noResources`)" />
      <div v-else class="table-scroll" tabindex="0" :aria-label="t(`${prefix}listeners`)"><table><thead><tr><th>{{ t(`${prefix}name`) }}</th><th>{{ t(`${prefix}node`) }}</th><th>{{ t(`${prefix}actualEndpoint`) }}</th><th>{{ t(`${prefix}configuration`) }}</th><th>{{ t(`${prefix}references`) }}</th></tr></thead><tbody>
        <tr v-for="listener in resources.listeners" :key="listener.id"><td>{{ listener.name }}<small>{{ listener.id }} · r{{ listener.revision }}</small></td><td><EntityLink kind="node" :id="listener.nodeId" :label="nodeName(workspace, listener.nodeId)" /><br><RouterLink :to="pageLocation('node-detail', { nodeId: listener.nodeId }, { tab: 'protocols' })">{{ t(`${prefix}protocolsPorts`) }}</RouterLink></td><td class="mono">{{ t(`features.routes.protocols.${listener.protocol}`) }}<br>{{ listener.address }}:{{ listener.port }} · {{ listener.transport }}<small>{{ listener.id === route.published?.entryListenerId || listener.id === route.draft.entryListenerId ? t(`${prefix}entry`) : (listener.entryIds?.length ? t(`${prefix}subscriptionEntry`) : t(`${prefix}internal`)) }}</small></td><td><StatusBadge :status="listener.state ?? 'draft'" :label="listener.state ? t(`${prefix}states.${listener.state}`) : t(`${prefix}unknown`)" /><small>{{ t(`${prefix}cleanup`) }}: {{ t(`${prefix}cleanupStates.${listener.cleanup ?? 'none'}`) }}</small></td><td><div v-for="id in listener.routeIds" :key="id"><EntityLink kind="route" :id="id" :label="workspace.routes.find(item => item.id === id)?.name ?? id" /></div><small>{{ t(`${prefix}entryReferences`, { count: workspace.entries.filter(entry => entry.listenerId === listener.id).length }) }}</small></td></tr>
      </tbody></table></div>
    </section>
    <section class="panel"><h2>{{ t(`${prefix}tunnels`) }}</h2>
      <EmptyState v-if="!resources.tunnels.length" :title="t(`${prefix}noTunnels`)" />
      <article v-for="tunnel in resources.tunnels" :key="tunnel.id" class="resource">
        <h3>{{ tunnel.name }} · r{{ tunnel.revision }}</h3><p><EntityLink kind="node" :id="tunnel.relayNodeId" :label="nodeName(workspace, tunnel.relayNodeId)" /> → <EntityLink kind="node" :id="tunnel.remoteNodeId" :label="nodeName(workspace, tunnel.remoteNodeId)" /></p>
        <p class="mono">TCP + mTLS · {{ tunnel.relayEndpoint }} · 127.0.0.1:{{ tunnel.mappingPort }}</p><p>{{ t(`${prefix}tunnelApplied`, { relay: t(`${prefix}${tunnel.relayApplied ? 'yes' : 'no'}`), remote: t(`${prefix}${tunnel.remoteApplied ? 'yes' : 'no'}`) }) }}</p>
        <p>{{ t(`${prefix}references`) }}: <span v-for="id in tunnel.routeIds" :key="id" class="reference"><EntityLink kind="route" :id="id" :label="workspace.routes.find(item => item.id === id)?.name ?? id" /></span></p><p class="muted">{{ t(`${prefix}reverseWarning`) }}</p>
      </article>
    </section>
    <section class="panel"><h2>{{ t(`${prefix}subscriptions`) }}</h2>
      <EmptyState v-if="!subscriptions.length && !entries.length" :title="t(`${prefix}noSubscriptions`)" />
      <div v-for="subscription in subscriptions" :key="subscription.id" class="resource"><EntityLink kind="subscription" :id="subscription.id" :label="subscription.name" /> · <StatusBadge :status="subscription.state" /> · <StatusBadge :status="subscription.sync" /></div>
      <div v-for="entry in entries" :key="entry.id" class="resource"><EntityLink kind="subscription" :id="entry.subscriptionId" :label="entry.name" /> · <span class="mono">{{ entry.listenerId }}:{{ entry.port }}</span> · <StatusBadge :status="entry.identityState" /><small>{{ t(`${prefix}cleanup`) }}: {{ t(`${prefix}cleanupStates.${entry.cleanup ?? 'none'}`) }}</small></div>
    </section>
  </div>
</template>
<style scoped>
.resources { display: grid; gap: 16px; }h2 { font-size: 17px; margin-top: 0; }h3 { font-size: 15px; }.table-scroll { overflow-x: auto; }table { width: 100%; min-width: 720px; border-collapse: collapse; }th, td { padding: 12px; border-bottom: 1px solid #e1e9e4; text-align: left; vertical-align: top; }th { color: #63736a; font-size: 12px; }small { display: block; color: #63736a; margin-top: 6px; }.mono { font-family: ui-monospace, monospace; overflow-wrap: anywhere; }.resource { padding: 12px 0; border-bottom: 1px solid #e1e9e4; }.reference { margin-right: 12px; }.muted { color: #63736a; font-size: 13px; }
</style>
