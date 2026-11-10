<script setup lang="ts">
defineProps<{ id: string; label: string; error?: string; hint?: string }>()
</script>
<template>
  <div class="field" :class="{ invalid: error }">
    <label :id="`${id}-label`" :for="id">{{ label }}</label>
    <slot />
    <p v-if="hint" :id="`${id}-hint`" class="hint">{{ hint }}</p>
    <p v-if="error" :id="`${id}-error`" role="alert" class="error">{{ error }}</p>
  </div>
</template>
<style scoped>
.field { min-width: 0; margin-bottom: 20px; } label { font-weight: 500; } .hint, .error { margin: 6px 0 0; font-size: 12px; line-height: 1.5; } .hint { color: var(--color-muted); } .error { color: #ab332c; } .invalid { border-left: 2px solid #ab332c; padding-left: 12px; }
</style>
