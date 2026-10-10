import { createRouter, createWebHistory } from 'vue-router'
import { authChecked, authEnabled, authenticated, refreshAuth } from '../authState'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'overview', component: () => import('../views/Overview.vue'), meta: { title: 'Overview' } },
    { path: '/servers', name: 'servers', component: () => import('../views/Servers.vue'), meta: { title: 'Servers' } },
    { path: '/servers/new', name: 'server-new', component: () => import('../views/ServerForm.vue'), meta: { title: 'Add server' } },
    {
      path: '/servers/:id/edit', name: 'server-edit', component: () => import('../views/ServerForm.vue'),
      props: (route) => ({ id: Number(route.params.id) }), meta: { title: 'Configure server' },
    },
    {
      path: '/servers/:id', name: 'server-detail', component: () => import('../views/ServerDetail.vue'),
      props: (route) => ({ id: Number(route.params.id) }), meta: { title: 'Server details' },
    },
    { path: '/benches', name: 'benches', component: () => import('../views/Benches.vue'), meta: { title: 'Benches' } },
    {
      path: '/benches/:server/:bench', name: 'bench-detail', component: () => import('../views/BenchDetail.vue'),
      props: (route) => ({ server: String(route.params.server), bench: String(route.params.bench) }),
      meta: { title: 'Bench details' },
    },
    { path: '/sites', name: 'sites', component: () => import('../views/Sites.vue'), meta: { title: 'Sites' } },
		{ path: '/logs', name: 'logs', component: () => import('../views/Logs.vue'), meta: { title: 'Log explorer' } },
    {
      path: '/sites/:server/:bench/:site', name: 'site-detail', component: () => import('../views/SiteDetail.vue'),
      props: (route) => ({ server: String(route.params.server), bench: String(route.params.bench), site: String(route.params.site) }),
      meta: { title: 'Site details' },
    },
    { path: '/alerts', name: 'alerts', component: () => import('../views/Alerts.vue'), meta: { title: 'Incidents' } },
    { path: '/settings', name: 'settings', component: () => import('../views/Settings.vue'), meta: { title: 'Settings' } },
    { path: '/login', name: 'login', component: () => import('../views/Login.vue'), meta: { layout: 'bare', title: 'Sign in' } },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
  scrollBehavior() { return { top: 0 } },
})

// Resolve authentication before mounting a protected view. Without this
// guard, the overview briefly started four fleet requests and each returned
// 401 before the fetch wrapper redirected to the login page.
router.beforeEach(async (to) => {
  if (to.meta.layout === 'bare') return true
  try {
		if (!authChecked.value) await refreshAuth()
    if (authEnabled.value && !authenticated.value) {
      return { name: 'login', query: { next: to.fullPath } }
    }
  } catch {
    // Let the requested page render its normal backend-unavailable state.
  }
  return true
})
