<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { RouterView, RouterLink, useRoute } from 'vue-router'
import {
  Server,
  Boxes,
  Globe,
  Database,
  Bell,
  SlidersHorizontal,
  Activity,
  LogOut,
  Menu,
  X,
} from 'lucide-vue-next'
import TimelineFilter from './components/TimelineFilter.vue'
import { logout } from './api'
import { realtime } from './realtime'

const route = useRoute()
const bare = computed(() => route.meta.layout === 'bare')

const nav = [
  { to: '/servers', label: 'Servers', icon: Server },
  { to: '/benches', label: 'Benches', icon: Boxes },
  { to: '/sites', label: 'Sites', icon: Globe },
  { to: '/databases', label: 'Databases', icon: Database },
  { to: '/control', label: 'Control', icon: SlidersHorizontal },
  { to: '/alerts', label: 'Alerts', icon: Bell },
]

// Mobile off-canvas drawer. Closes on any route change so tapping a nav
// link doesn't leave the drawer covering the page you navigated to.
const drawerOpen = ref(false)
watch(
  () => route.fullPath,
  () => {
    drawerOpen.value = false
  },
)

// When the drawer is off-canvas (mobile + closed) its links are visually
// hidden by a CSS transform — but a transform doesn't remove them from the
// tab order or the a11y tree. Track the mobile breakpoint so we can mark the
// sidebar inert/aria-hidden in exactly that state (NOT on desktop, where the
// sidebar is always interactive).
const isMobile = ref(false)
const drawerHidden = computed(() => isMobile.value && !drawerOpen.value)
let mq: MediaQueryList | null = null
function syncMobile(e: MediaQueryList | MediaQueryListEvent) {
  isMobile.value = e.matches
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') drawerOpen.value = false
}
onMounted(() => {
  mq = window.matchMedia('(max-width: 900px)')
  syncMobile(mq)
  mq.addEventListener('change', syncMobile)
  window.addEventListener('keydown', onKey)
})
onUnmounted(() => {
  mq?.removeEventListener('change', syncMobile)
  window.removeEventListener('keydown', onKey)
})

async function handleLogout() {
  // Confirm so a misclick (especially on a phone) doesn't drop the
  // operator out of the dashboard mid-task.
  if (!window.confirm('Sign out of frappe-monitor?')) return
  // Stop the live socket so it doesn't reconnect against a now-invalid
  // session during the brief window before the hard navigation lands.
  realtime.disconnect()
  try {
    await logout()
  } catch {
    // Server-side cookie-clear failed; we still tear down the local view
    // and route to /login. Login.vue's ?signed_out=1 branch suppresses
    // the auto-redirect so a stale-but-valid cookie can't bounce back.
  }
  // Hard navigation so SPA state (cached fetch promises, in-flight refresh
  // timers) is dropped.
  window.location.assign('/login?signed_out=1')
}
</script>

<template>
  <RouterView v-if="bare" />
  <div v-else class="app-shell" :class="{ 'drawer-open': drawerOpen }">
    <!-- Backdrop: only visible/interactive when the mobile drawer is open. -->
    <div class="overlay" @click="drawerOpen = false" />

    <aside class="sidebar" :inert="drawerHidden || undefined" :aria-hidden="drawerHidden || undefined">
      <div class="brand">
        <span class="brand-mark"><Activity :size="18" :stroke-width="2.5" /></span>
        <div class="brand-text">
          <h1>frappe&#8203;monitor</h1>
          <span class="brand-sub">production</span>
        </div>
        <button class="drawer-close" type="button" aria-label="Close menu" @click="drawerOpen = false">
          <X :size="18" />
        </button>
      </div>

      <nav>
        <RouterLink v-for="item in nav" :key="item.to" :to="item.to" class="nav-link">
          <component :is="item.icon" :size="18" :stroke-width="2" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>

      <button class="nav-link logout" type="button" @click="handleLogout">
        <LogOut :size="18" :stroke-width="2" />
        <span>Sign out</span>
      </button>
    </aside>

    <div class="main">
      <header class="header">
        <button class="hamburger" type="button" aria-label="Open menu" @click="drawerOpen = true">
          <Menu :size="20" />
        </button>
        <TimelineFilter />
      </header>
      <main class="content">
        <RouterView />
      </main>
    </div>
  </div>
</template>

<style scoped>
.app-shell {
  display: grid;
  grid-template-columns: 240px 1fr;
  min-height: 100vh;
  overflow-x: hidden;
  overflow-x: clip;
  max-width: 100vw;
}

/* ---- Sidebar ----------------------------------------------------- */
.sidebar {
  background: var(--sidebar-bg);
  color: var(--sidebar-fg);
  padding: 1.1rem 0.8rem 1rem;
  border-right: 1px solid var(--card-border);
  display: flex;
  flex-direction: column;
  position: sticky;
  top: 0;
  height: 100vh;
  z-index: 30;
}
.brand {
  display: flex;
  align-items: center;
  gap: 0.65rem;
  padding: 0.25rem 0.5rem 1.1rem;
  border-bottom: 1px solid var(--card-border);
  margin-bottom: 0.9rem;
}
.brand-mark {
  display: grid;
  place-items: center;
  width: 34px;
  height: 34px;
  border-radius: 10px;
  background-image: var(--accent-grad);
  color: #fff;
  box-shadow: 0 4px 14px -4px var(--accent);
  flex-shrink: 0;
}
.brand-text { min-width: 0; }
.brand-text h1 {
  font-size: 1rem;
  margin: 0;
  font-weight: 650;
  letter-spacing: -0.02em;
  color: var(--fg-strong);
  white-space: nowrap;
}
.brand-sub {
  font-size: 0.66rem;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.12em;
  font-weight: 600;
}
.drawer-close {
  display: none;
  margin-left: auto;
  background: transparent;
  border: none;
  color: var(--muted);
  cursor: pointer;
  padding: 0.25rem;
}

nav {
  display: flex;
  flex-direction: column;
  gap: 0.12rem;
}
.nav-link {
  position: relative;
  display: flex;
  align-items: center;
  gap: 0.7rem;
  padding: 0.6rem 0.7rem;
  border-radius: var(--radius-sm);
  color: var(--muted);
  text-decoration: none;
  font-size: 0.9rem;
  font-weight: 550;
  transition: background var(--transition), color var(--transition);
}
.nav-link:hover {
  background: var(--bg-hover);
  color: var(--fg);
}
.nav-link.router-link-active {
  background: var(--sidebar-active);
  color: var(--accent-strong);
}
/* Left accent bar on the active route. */
.nav-link.router-link-active::before {
  content: '';
  position: absolute;
  left: -0.8rem;
  top: 50%;
  transform: translateY(-50%);
  width: 3px;
  height: 1.1rem;
  border-radius: 0 3px 3px 0;
  background-image: var(--accent-grad);
}
.logout {
  margin-top: auto;
  background: transparent;
  border: 1px solid var(--card-border);
  cursor: pointer;
  width: 100%;
  text-align: left;
}
.logout:hover {
  border-color: var(--status-unreachable);
  color: var(--status-unreachable);
  background: var(--status-unreachable-soft);
}

/* ---- Main column ------------------------------------------------- */
.main {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  min-width: 0;
  overflow-x: hidden;
  overflow-x: clip;
}
.header {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.7rem 1.75rem;
  border-bottom: 1px solid var(--card-border);
  background: color-mix(in srgb, var(--bg) 80%, transparent);
  backdrop-filter: blur(8px);
  position: sticky;
  top: 0;
  z-index: 10;
}
.hamburger {
  display: none;
  background: var(--bg-elevated);
  border: 1px solid var(--card-border-strong);
  color: var(--fg);
  border-radius: var(--radius-sm);
  padding: 0.4rem;
  cursor: pointer;
}
.content {
  padding: 1.75rem;
  flex: 1;
  min-width: 0;
  max-width: 100%;
}

/* Backdrop for the mobile drawer. */
.overlay {
  display: none;
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.55);
  z-index: 25;
  opacity: 0;
  transition: opacity var(--transition);
}

/* ---- Tablet ------------------------------------------------------ */
@media (max-width: 1024px) {
  .app-shell { grid-template-columns: 216px 1fr; }
  .content { padding: 1.35rem; }
  .header { padding: 0.7rem 1.35rem; }
}

/* ---- Mobile: sidebar becomes an off-canvas drawer ---------------- */
@media (max-width: 900px) {
  .app-shell { grid-template-columns: 1fr; }
  .sidebar {
    position: fixed;
    top: 0;
    left: 0;
    width: 264px;
    max-width: 82vw;
    transform: translateX(-100%);
    transition: transform var(--transition);
    box-shadow: 0 0 40px rgba(0, 0, 0, 0.5);
  }
  .drawer-open .sidebar { transform: translateX(0); }
  .drawer-close { display: block; }
  .hamburger { display: inline-flex; }
  .overlay { display: block; pointer-events: none; }
  .drawer-open .overlay { opacity: 1; pointer-events: auto; }
  .header { padding: 0.6rem 1rem; }
  .content { padding: 1rem; }
}

@media (max-width: 480px) {
  .content { padding: 0.85rem; }
}
</style>
