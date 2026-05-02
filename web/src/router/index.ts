import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/servers' },
    {
      path: '/servers',
      name: 'servers',
      component: () => import('../views/Servers.vue'),
    },
    {
      path: '/servers/:id',
      name: 'server-detail',
      component: () => import('../views/ServerDetail.vue'),
      props: (route) => ({ id: Number(route.params.id) }),
    },
    {
      path: '/benches',
      name: 'benches',
      component: () => import('../views/Benches.vue'),
    },
    {
      path: '/benches/:server/:bench',
      name: 'bench-detail',
      component: () => import('../views/BenchDetail.vue'),
      props: (route) => ({
        server: String(route.params.server),
        bench: String(route.params.bench),
      }),
    },
    {
      path: '/sites',
      name: 'sites',
      component: () => import('../views/Sites.vue'),
    },
    {
      path: '/sites/:server/:bench/:site',
      name: 'site-detail',
      component: () => import('../views/SiteDetail.vue'),
      props: (route) => ({
        server: String(route.params.server),
        bench: String(route.params.bench),
        site: String(route.params.site),
      }),
    },
    {
      path: '/alerts',
      name: 'alerts',
      component: () => import('../views/Alerts.vue'),
    },
  ],
})
