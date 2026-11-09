import { createRouter, createWebHashHistory, type LocationQueryRaw } from 'vue-router'
import { pageRoutes, type PageName } from './routes'
import { sanitizeQuery, createSafeReturnTo } from './query'
import { i18n } from '../i18n'
export const router = createRouter({ history: createWebHashHistory(), routes: [
 { path: '/', redirect: '/overview' }, { path: '/client-config', redirect: '/client-config/subscriptions' },
 ...pageRoutes, { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('../components/NotFoundPage.vue'), meta: { titleKey: 'common.notFound' } },
], scrollBehavior: (_to, _from, saved) => saved ?? { top: 0 } })
export const safeReturnTo = createSafeReturnTo(router.resolve)
export function pageLocation(name: PageName, params: Record<string, string> = {}, query: LocationQueryRaw = {}) {
 const clean = sanitizeQuery(name, query); const back = safeReturnTo(query.returnTo)
 if (back && ['login', 'profile-new', 'profile-edit', 'subscription-new', 'subscription-edit'].includes(name)) clean.returnTo = back
 return { name, params, query: clean }
}
export function entityLocation(kind: 'node' | 'route' | 'subscription' | 'profile' | 'release', id: string) {
 const names = { node: ['node-detail', 'nodeId'], route: ['route-detail', 'routeId'], subscription: ['subscription-detail', 'subscriptionId'], profile: ['profile-edit', 'profileId'], release: ['release-detail', 'releaseId'] } as const
 const [name, param] = names[kind]; return pageLocation(name, { [param]: id })
}
router.beforeEach(to => {
 const clean = sanitizeQuery(String(to.name), to.query)
 const back = safeReturnTo(to.query.returnTo)
 if (back && ['login', 'profile-new', 'profile-edit', 'subscription-new', 'subscription-edit'].includes(String(to.name))) clean.returnTo = back
 if (JSON.stringify(clean) !== JSON.stringify(to.query)) return { path: to.path, query: clean, hash: '', replace: true }
 if (to.hash) return { path: to.path, query: clean, hash: '', replace: true }
})
router.afterEach(to => { document.title = `${i18n.global.t(String(to.meta.titleKey))} · Meshstrata` })
