import { createI18n } from 'vue-i18n'
import en from './locales/en'
const features: Record<string, unknown> = {}
const modules = import.meta.glob<{ default: Record<string, unknown> }>('./features/*/locales/en.ts', { eager: true })
for (const [path, module] of Object.entries(modules)) features[path.split('/')[2]!] = module.default
export const i18n = createI18n({ legacy: false, locale: 'en', fallbackLocale: 'en', messages: { en: { ...en, features } } })
