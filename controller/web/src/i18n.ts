import { watch } from 'vue'
import { createI18n } from 'vue-i18n'
import en from './locales/en'
import zhCN from './locales/zh-CN'
import { workspace } from './demo/workspace'
import { resolveLocale } from './locale'
function featureMessages(language: 'en' | 'zh-CN') {
  const features: Record<string, unknown> = {}
  const modules = import.meta.glob<{ default: Record<string, unknown> }>('./features/*/locales/{en,zh-CN}.ts', { eager: true })
  for (const [path, module] of Object.entries(modules)) if (path.endsWith(`/${language}.ts`)) features[path.split('/')[2]!] = module.default
  return features
}
export const i18n = createI18n({
  legacy: false, locale: resolveLocale(workspace.settings.locale), fallbackLocale: false,
  messages: { en: { ...en, features: featureMessages('en') }, 'zh-CN': { ...zhCN, features: featureMessages('zh-CN') } },
})
watch(() => workspace.settings.locale, saved => {
  const locale = resolveLocale(saved)
  i18n.global.locale.value = locale
  document.documentElement.lang = locale
}, { immediate: true, flush: 'sync' })
