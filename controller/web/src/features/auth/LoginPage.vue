<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NCheckbox, NInput } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { pageLocation, safeReturnTo } from '../../app/router'
import { resetEpoch, useWorkspace } from '../../demo/workspace'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
const { t } = useI18n()
const text = (key: string) => t(`features.auth.${key}`)
const { workspace, sessionSecrets } = useWorkspace()
const route = useRoute(), router = useRouter(), task = useSimulatedTask()
const account = ref('admin'), showPassword = ref(false), accountError = ref(false), passwordError = ref(false), failed = ref(false)
const password = computed({ get: () => sessionSecrets.get('auth:input:password') ?? '', set: (value: string) => { if (value) sessionSecrets.set('auth:input:password', value); else sessionSecrets.delete('auth:input:password') } })
const returnTarget = computed(() => safeReturnTo(route.query.returnTo))
const returnLabel = computed(() => returnTarget.value ?? text('defaultReturn'))
async function enterDemo() {
  task.cancel(); sessionSecrets.delete('auth:input:password')
  sessionSecrets.set('auth:demo-session', 'admin')
  // Revalidate immediately before consumption; the helper strips unapproved query fields.
  await router.push(safeReturnTo(route.query.returnTo) ?? pageLocation('overview'))
}
async function signIn() {
  if (task.state.value === 'running') return
  accountError.value = account.value.trim() !== 'admin'
  passwordError.value = !password.value || password.value !== (sessionSecrets.get('auth:admin-password') ?? 'demo-only')
  failed.value = false
  if (accountError.value || passwordError.value) {
    document.getElementById(accountError.value ? 'demo-account' : 'demo-password')?.focus()
    return
  }
  const operationEpoch = resetEpoch.value
  const success = await task.run(workspace.scenario === 'error' ? 'error' : 'success', 500)
  if (operationEpoch !== resetEpoch.value) return
  if (success) await enterDemo()
  else { failed.value = task.state.value === 'error'; sessionSecrets.delete('auth:input:password') }
}
watch(resetEpoch, () => { task.cancel(); account.value = 'admin'; showPassword.value = false; accountError.value = false; passwordError.value = false; failed.value = false })
onBeforeUnmount(() => sessionSecrets.delete('auth:input:password'))
</script>
<template>
  <div class="login-shell">
    <div class="login-brand"><span class="brand-mark" aria-hidden="true">◇</span>{{ text('brand') }}</div>
    <section class="login-card" aria-labelledby="demo-login-title">
      <p class="eyebrow">{{ text('eyebrow') }}</p>
      <h1 id="demo-login-title">{{ text('LoginPage.title') }}</h1>
      <p class="intro">{{ text('LoginPage.goal') }}</p>
      <NAlert type="info">{{ text('notice') }}</NAlert>
      <form @submit.prevent="signIn">
        <div class="field"><label for="demo-account">{{ text('account') }}</label><NInput v-model:value="account" :disabled="task.state.value === 'running'" autocomplete="off" :maxlength="80" :input-props="{ id: 'demo-account', 'aria-invalid': accountError, 'aria-describedby': accountError ? 'demo-account-error' : undefined }" :status="accountError ? 'error' : undefined" /><p v-if="accountError" id="demo-account-error" role="alert" class="error">{{ text('errorAccount') }}</p></div>
        <div class="field"><label for="demo-password">{{ text('password') }}</label><NInput v-model:value="password" :disabled="task.state.value === 'running'" :type="showPassword ? 'text' : 'password'" autocomplete="off" :maxlength="128" :input-props="{ id: 'demo-password', 'aria-invalid': passwordError, 'aria-describedby': passwordError ? 'demo-password-error demo-password-hint' : 'demo-password-hint' }" :status="passwordError ? 'error' : undefined" /><p v-if="passwordError" id="demo-password-error" role="alert" class="error">{{ text('errorPassword') }}</p></div>
        <NCheckbox v-model:checked="showPassword">{{ text('showPassword') }}</NCheckbox>
        <p id="demo-password-hint" class="help">{{ text('passwordHint') }}</p>
        <NAlert v-if="failed" type="error" role="alert">{{ text('failed') }}</NAlert><p v-if="task.state.value === 'cancelled'" role="status">{{ text('cancelled') }}</p>
        <div class="login-actions"><NButton type="primary" @click="enterDemo">{{ text('enterDemo') }}</NButton><NButton attr-type="submit" :loading="task.state.value === 'running'">{{ text('signIn') }}</NButton><NButton v-if="task.state.value === 'running'" @click="task.cancel(); password = ''">{{ text('cancel') }}</NButton></div>
      </form>
      <p class="help">{{ text('demoHint') }}</p>
      <p class="return-target">{{ text('returnHint') }}: <strong>{{ returnLabel }}</strong></p>
    </section>
    <p class="login-footer">{{ text('stateHint') }}</p>
  </div>
</template>
<style scoped>
.login-shell { width: 100%; max-width: 600px; margin: 0 auto; padding: 32px 20px; } .login-brand { display: flex; align-items: center; gap: 12px; margin: 0 0 28px; color: #183d32; font-size: 22px; font-weight: 650; } .brand-mark { font-size: 32px; color: var(--color-primary); } .login-card { background: var(--color-surface); border: 1px solid var(--color-border); border-radius: var(--radius); padding: 32px; } .eyebrow { margin-top: 0; color: var(--color-primary); font-size: 11px; letter-spacing: .6px; } h1 { font-size: 30px; } .intro { color: var(--color-muted); margin-bottom: 24px; } form { margin-top: 28px; } .field { margin-bottom: 20px; } label { font-weight: 500; } .help, .login-footer { font-size: 12px; color: var(--color-muted); line-height: 1.7; } .error { font-size: 12px; color: #ab332c; margin: 8px 0; } .login-actions { display: flex; flex-wrap: wrap; gap: 12px; margin-top: 24px; } .login-actions :deep(.n-button) { min-height: 44px; flex: 1; } .return-target { padding-top: 20px; border-top: 1px solid var(--color-border); font-size: 12px; overflow-wrap: anywhere; color: var(--color-muted); } .login-footer { margin-top: 24px; text-align: center; }
@media (max-width: 480px) { .login-shell { padding: 24px 8px; } .login-card { padding: 24px 16px; } .login-actions { flex-direction: column; } }
</style>
