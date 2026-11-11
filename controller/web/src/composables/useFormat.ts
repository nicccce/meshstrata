import { useI18n } from 'vue-i18n'
export function useFormat() {
 const { locale, t } = useI18n()
 return { date(value?: string | null) { const d = value ? new Date(value) : undefined; return d && Number.isFinite(d.getTime()) ? new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(d) : t('common.unknown') }, number(value?: number | null) { return value == null ? t('common.unknown') : new Intl.NumberFormat(locale.value).format(value) } }
}
