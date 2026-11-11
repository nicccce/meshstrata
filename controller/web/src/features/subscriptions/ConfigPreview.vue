<script setup lang="ts">
import { computed, ref } from 'vue'
import { NAlert, NButton, NInput, NRadioGroup, NRadioButton, NTag, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { Draft } from '../../domain'
import { useWorkspace } from '../../demo/workspace'
import { renderConfig, downloadExample, type Plan } from './model'
const props = defineProps<{ draft: Draft; plan: Plan; blocked?: boolean }>()
const { workspace } = useWorkspace(); const { t } = useI18n(); const message = useMessage()
const tr = (key: string) => t(`features.subscriptions.${key}`)
const format = ref('full'); const copyFallback = ref(false)
const output = computed(() => renderConfig(workspace, props.draft, props.plan, format.value === 'provider'))
async function copy() { try { await navigator.clipboard.writeText(output.value); message.success(tr('copied')) } catch { copyFallback.value = true; message.warning(tr('copyFailed')) } }
function download(provider: boolean) { if (!props.blocked && !props.plan.errors.length) downloadExample(renderConfig(workspace, props.draft, props.plan, provider), provider ? 'meshstrata-provider-example.yaml' : 'meshstrata-full-example.yaml') }
</script>
<template>
  <NAlert type="info" :bordered="false">{{ tr('exampleNotice') }}</NAlert>
  <div class="actions">
    <NRadioGroup v-model:value="format" :aria-label="tr('config')"><NRadioButton value="full">{{ tr('full') }}</NRadioButton><NRadioButton value="provider">{{ tr('provider') }}</NRadioButton></NRadioGroup>
    <NButton :disabled="blocked || !!plan.errors.length" @click="download(false)">{{ tr('download') }}</NButton>
    <NButton :disabled="blocked || !!plan.errors.length" @click="download(true)">{{ tr('downloadProvider') }}</NButton>
    <NButton :disabled="blocked || !!plan.errors.length" @click="copy">{{ tr('copyConfig') }}</NButton>
  </div>
  <p v-if="blocked || plan.errors.length" role="status">{{ tr('noConfig') }}</p>
  <template v-if="format === 'full'">
    <div class="sections">
      <details><summary>{{ tr('proxies') }} · {{ plan.entries.length }}</summary><div v-for="entry in plan.entries" :key="entry.entry.id"><code>{{ entry.entry.id }}</code> — {{ entry.server }}:{{ entry.entry.port }} <NTag size="small">{{ entry.listener.protocol }}</NTag></div></details>
      <details><summary>{{ tr('groups') }} · {{ plan.groups.length }}</summary><p v-for="group in plan.groups" :key="group.name"><code>{{ group.name }}</code> → {{ group.routeIds.join(', ') }} · {{ tr('initial') }}: {{ group.selectedRouteId }}</p></details>
      <details><summary>{{ tr('dns') }}</summary><p v-if="!Object.keys(plan.dns).length">{{ tr('noDns') }}</p><p v-for="(servers, domain) in plan.dns" :key="domain"><code>{{ domain }}</code> → {{ servers.join(', ') }}</p></details>
      <details><summary>{{ tr('rules') }} · {{ plan.rules.length }}</summary><ol><li v-for="(rule, index) in plan.rules" :key="index"><code>{{ rule }}</code></li></ol></details>
    </div>
  </template>
  <p v-if="copyFallback" role="status">{{ tr('copyFailed') }}</p>
  <NInput :value="output" type="textarea" readonly :autosize="{ minRows: 12, maxRows: 28 }" :aria-label="tr(format === 'provider' ? 'provider' : 'full')" @focus="($event.target as HTMLTextAreaElement)?.select()" />
</template>
<style scoped>
.sections { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin: 20px 0; }
details { border: 1px solid var(--color-border); border-radius: 8px; padding: 0 16px 12px; overflow-wrap: anywhere; }
code { font-size: 12px; } ol { padding-left: 20px; line-height: 1.8; }
@media (max-width: 760px) { .sections { grid-template-columns: 1fr; } }
</style>
