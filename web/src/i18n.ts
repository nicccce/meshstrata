import { createI18n } from 'vue-i18n'
import en from './locales/en'

// Add locale resources here without changing component strings or data IDs.
export const i18n = createI18n({
  legacy: false,
  locale: 'en',
  fallbackLocale: 'en',
  messages: { en },
})
