<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { NButton, NConfigProvider, NSpin, NTag, enUS, dateEnUS } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { getSystem, type SystemInfo } from './api'

const { t, locale } = useI18n()
const pages = ['dashboard', 'nodes', 'routes', 'ruleProfiles', 'subscriptions', 'activity', 'settings'] as const
type Page = typeof pages[number]
const page = ref<Page>('dashboard')
const system = ref<SystemInfo | null>(null)
const loading = ref(false)
const failed = ref(false)
const queriedAt = ref<Date | null>(null)
let request: AbortController | undefined
const theme = { common: { primaryColor: '#207665', primaryColorHover: '#2a8d79', primaryColorPressed: '#155545', borderRadius: '8px' } }
const dateFormatter = computed(() => new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'medium' }))
const queryTime = computed(() => queriedAt.value ? dateFormatter.value.format(queriedAt.value) : '')
const buildTime = computed(() => {
  const value = system.value?.build_time
  if (!value || value === 'unknown') return t('dashboard.unknown')
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? t('dashboard.unknown') : dateFormatter.value.format(date)
})

function display(value: string): string {
  if (value === 'dev') return t('dashboard.development')
  if (value === 'unknown') return t('dashboard.unknown')
  return value
}

async function refresh() {
  request?.abort()
  const current = new AbortController()
  request = current
  loading.value = true
  failed.value = false
  const timeout = window.setTimeout(() => current.abort(), 5000)
  try {
    const result = await getSystem(current.signal)
    if (request === current) {
      system.value = result
      queriedAt.value = new Date()
    }
  } catch {
    if (request === current && page.value === 'dashboard') {
      system.value = null
      failed.value = true
    }
  } finally {
    window.clearTimeout(timeout)
    if (request === current) loading.value = false
  }
}

watch(page, (next) => {
  if (next === 'dashboard') void refresh()
  else {
    request?.abort()
    request = undefined
    loading.value = false
  }
}, { immediate: true })
onBeforeUnmount(() => request?.abort())
</script>

<template>
  <NConfigProvider :locale="enUS" :date-locale="dateEnUS" :theme-overrides="theme">
    <div class="workspace">
      <aside class="sidebar">
        <a class="brand" href="#" @click.prevent="page = 'dashboard'">
          <span class="brand-mark" aria-hidden="true"><span></span><span></span><span></span></span>
          <span>{{ t('app.name') }}<small>{{ t('app.label') }}</small></span>
        </a>
        <nav :aria-label="t('app.navigation')">
          <button v-for="(item, index) in pages" :key="item" :class="['nav-item', { active: page === item }]" :aria-current="page === item ? 'page' : undefined" @click="page = item">
            <span class="nav-symbol" aria-hidden="true">{{ ['◫', '◈', '⇄', '≡', '⊞', '◷', '⚙'][index] }}</span>
            {{ t(`nav.${item}`) }}
          </button>
        </nav>
        <div class="sidebar-footer"><span class="environment-dot" aria-hidden="true"></span>{{ t('app.environment') }}</div>
      </aside>
      <main>
        <header class="topbar"><span>{{ t(`nav.${page}`) }}</span><NTag size="small" :bordered="false">{{ t('app.stage') }}</NTag></header>
        <section class="content">
          <template v-if="page === 'dashboard'">
            <div class="page-heading"><p class="eyebrow">{{ t('dashboard.eyebrow') }}</p><h1>{{ t('nav.dashboard') }}</h1><p>{{ t('dashboard.description') }}</p></div>
            <section class="runtime-card" aria-live="polite">
              <div class="card-heading"><div><h2>{{ t('dashboard.system') }}</h2><p>{{ t('dashboard.systemDescription') }}</p></div><NButton :loading="loading" :disabled="loading" @click="refresh">{{ t('dashboard.refresh') }}</NButton></div>
              <div v-if="loading" class="status-panel"><NSpin size="small" /><span>{{ t('dashboard.loading') }}</span></div>
              <div v-else-if="failed" class="status-panel error" role="alert"><p>{{ t('dashboard.unavailable') }}</p><NButton @click="refresh">{{ t('dashboard.retry') }}</NButton></div>
              <dl v-else-if="system" class="system-grid">
                <div><dt>{{ t('dashboard.component') }}</dt><dd>{{ t('dashboard.controller') }}</dd></div>
                <div><dt>{{ t('dashboard.version') }}</dt><dd>{{ display(system.version) }}</dd></div>
                <div><dt>{{ t('dashboard.commit') }}</dt><dd class="mono">{{ display(system.commit) }}</dd></div>
                <div><dt>{{ t('dashboard.buildTime') }}</dt><dd>{{ buildTime }}</dd></div>
                <div class="instance"><dt>{{ t('dashboard.instanceId') }}</dt><dd class="mono">{{ system.instance_id }}</dd></div>
              </dl>
              <div v-if="queriedAt && !failed && !loading" class="card-footer">{{ t('dashboard.requested', { time: queryTime }) }}</div>
            </section>
            <div class="notes-grid"><article><span class="note-icon" aria-hidden="true">↗</span><h2>{{ t('dashboard.manual') }}</h2><p>{{ t('dashboard.manualDescription') }}</p></article><article><span class="note-icon" aria-hidden="true">◈</span><h2>{{ t('dashboard.independent') }}</h2><p>{{ t('dashboard.independentDescription') }}</p></article></div>
          </template>
          <template v-else>
            <div class="page-heading"><p class="eyebrow">{{ t('placeholder.label') }}</p><h1>{{ t(`nav.${page}`) }}</h1></div>
            <section class="placeholder-card"><span class="placeholder-symbol" aria-hidden="true">◇</span><h2>{{ t('placeholder.title', { page: t(`nav.${page}`) }) }}</h2><p>{{ t('placeholder.description') }}</p></section>
          </template>
          <footer class="page-footer">{{ t('app.boundary') }}</footer>
        </section>
      </main>
    </div>
  </NConfigProvider>
</template>
