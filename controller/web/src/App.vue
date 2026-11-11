<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NConfigProvider, NMessageProvider, NDialogProvider, enUS, dateEnUS, zhCN, dateZhCN } from 'naive-ui'
import AppLayout from './app/AppLayout.vue'
const { locale } = useI18n()
const uiLocale = computed(() => locale.value === 'zh-CN' ? zhCN : enUS)
const dateLocale = computed(() => locale.value === 'zh-CN' ? dateZhCN : dateEnUS)
const theme = { common: { primaryColor: '#207665', primaryColorHover: '#2a8d79', primaryColorPressed: '#155545', borderRadius: '8px' } }
</script>
<template><NConfigProvider :locale="uiLocale" :date-locale="dateLocale" :theme-overrides="theme"><NMessageProvider><NDialogProvider><AppLayout /></NDialogProvider></NMessageProvider></NConfigProvider></template>
