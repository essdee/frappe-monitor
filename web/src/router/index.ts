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
    // Phase 5+ adds /benches, /sites, /alerts, /settings, etc.
  ],
})
