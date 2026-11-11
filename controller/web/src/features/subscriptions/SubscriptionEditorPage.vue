<script setup lang="ts">
import { useRoute } from 'vue-router'
import { resetEpoch } from '../../demo/workspace'
import SubscriptionEditor from './SubscriptionEditor.vue'
const route = useRoute()
</script>
<template><SubscriptionEditor :key="`${String(route.params.subscriptionId ?? 'new')}:${String(route.query.draftId ?? 'unsaved')}:${resetEpoch}`" /></template>
