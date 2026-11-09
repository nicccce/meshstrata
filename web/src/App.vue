<script setup lang="ts">
import { NConfigProvider, NMessageProvider, NDialogProvider, enUS, dateEnUS } from 'naive-ui'
import AppLayout from './app/AppLayout.vue'
const theme = { common: { primaryColor: '#207665', primaryColorHover: '#2a8d79', primaryColorPressed: '#155545', borderRadius: '8px' } }
</script>
<template><NConfigProvider :locale="enUS" :date-locale="dateEnUS" :theme-overrides="theme"><NMessageProvider><NDialogProvider><AppLayout /></NDialogProvider></NMessageProvider></NConfigProvider></template>
