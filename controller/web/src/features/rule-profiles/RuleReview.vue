<script setup lang="ts">
import { computed, ref } from 'vue'
import { NAlert, NButton, NTabPane, NTabs } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { serializeRules, type analyzeRules, type RuleIssue } from './rules'
import DomainSetsPanel from './DomainSetsPanel.vue'
const props = defineProps<{ result: ReturnType<typeof analyzeRules>; selectedSets: string[]; readonly?: boolean }>()
const emit = defineEmits<{ focus: [line: number]; 'update:selectedSets': [ids: string[]] }>()
const { t } = useI18n(); const tr = (key: string) => t(`features.rule-profiles.${key}`)
const tab = ref('parsed')
defineExpose({ showValidation: () => { tab.value = 'validation' } })
const errors = computed(() => props.result.issues.filter(issue => !issue.warning))
const warnings = computed(() => props.result.issues.filter(issue => issue.warning))
function issueText(issue: RuleIssue) { return t(`features.rule-profiles.errors.${issue.code}`, { target: issue.target ?? '', detail: issue.detail ?? '' }) }
</script>
<template>
  <NTabs v-model:value="tab" type="line" animated>
    <NTabPane name="parsed" :tab="tr('parsed')">
      <p>{{ tr('udp') }}</p>
      <div class="table-scroll" v-if="result.lines.length"><table><thead><tr><th>{{ tr('line') }}</th><th>{{ tr('type') }}</th><th>{{ tr('value') }}</th><th>{{ tr('target') }}</th><th>{{ tr('options') }}</th></tr></thead><tbody><tr v-for="rule in result.lines" :key="rule.id"><td><NButton text @click="emit('focus', rule.line)">{{ rule.line }}</NButton></td><td>{{ rule.type }}</td><td>{{ rule.value || '—' }}</td><td>{{ rule.target }}</td><td>{{ rule.options?.join(', ') || '—' }}</td></tr></tbody></table></div>
      <p v-else>{{ tr('noRules') }}</p>
      <h3>{{ tr('generated') }}</h3><pre :class="{ invalid: !result.valid }">{{ serializeRules(result.generated) }}</pre>
    </NTabPane>
    <NTabPane name="dns" :tab="tr('dns')">
      <NAlert type="info" :bordered="false">{{ tr('dnsBoundary') }}</NAlert>
      <div v-if="result.mappings.length" class="table-scroll"><table><thead><tr><th>{{ tr('domain') }}</th><th>{{ tr('target') }}</th><th>{{ tr('source') }}</th></tr></thead><tbody><tr v-for="(map, index) in result.mappings" :key="index"><td><code>{{ map.suffix ? '+.' : '' }}{{ map.domain }}</code></td><td>{{ map.target }}<small v-if="map.target === 'REJECT'">{{ tr('rejectDns') }}</small><small v-if="map.target === 'DIRECT'">{{ tr('directDns') }}</small></td><td>{{ map.source }}<NButton v-if="map.line" text @click="emit('focus', map.line)"> · {{ tr('line') }} {{ map.line }}</NButton></td></tr></tbody></table></div><p v-else>{{ tr('noDns') }}</p>
      <p v-for="rule in result.uninferred" :key="rule.id"><strong>{{ tr('line') }} {{ rule.line }} · {{ rule.type }}:</strong> {{ tr('inference') }}</p>
    </NTabPane>
    <NTabPane name="sets" :tab="tr('sets')"><DomainSetsPanel :selected="selectedSets" :targets="result.catalog.rows.map(row => row.name)" :readonly="readonly" @update:selected="emit('update:selectedSets', $event)" /></NTabPane>
    <NTabPane name="validation" :tab="tr('validation')">
      <NAlert :type="errors.length ? 'error' : 'success'" :bordered="false">{{ errors.length ? t('features.rule-profiles.errorCount', { count: errors.length }) : tr('valid') }}</NAlert>
      <p v-if="warnings.length">{{ t('features.rule-profiles.warningCount', { count: warnings.length }) }}</p>
      <ul class="issues"><li v-for="(issue, index) in result.issues" :key="index" :class="{ warning: issue.warning }"><NButton v-if="issue.line" text @click="emit('focus', issue.line)">{{ tr('line') }} {{ issue.line }}: </NButton> {{ issueText(issue) }}</li></ul>
    </NTabPane>
  </NTabs>
</template>
<style scoped>
table{width:100%;border-collapse:collapse;font-size:14px}th,td{text-align:left;padding:12px;border-bottom:1px solid var(--color-border);vertical-align:top}small{display:block;max-width:300px;color:var(--color-muted)}pre{padding:16px;background:#f4f8f5;white-space:pre-wrap;overflow-wrap:anywhere;border-radius:8px}.invalid{border:1px dashed #b7791f}.issues{padding-left:24px}.issues li{padding:8px 0;color:#a52c2c}.issues .warning{color:#8a5c16}.table-scroll{margin-top:16px}code{overflow-wrap:anywhere}
@media(max-width:760px){:deep(.n-button){min-height:44px}}
</style>
