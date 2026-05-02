<script setup lang="ts">
import { computed } from 'vue'
import { RouterView, RouterLink, useRoute } from 'vue-router'
import {
  Server,
  Boxes,
  Globe,
  Bell,
  Activity,
  LogOut,
} from 'lucide-vue-next'
import TimelineFilter from './components/TimelineFilter.vue'
import { logout } from './api'

const route = useRoute()
const bare = computed(() => route.meta.layout === 'bare')

async function handleLogout() {
  await logout()
  // Pass a flag so Login.vue can show a "Signed out" confirmation —
  // otherwise the user just sees the login form and wonders if the
  // click did anything.
  window.location.assign('/login?signed_out=1')
}
</script>

<template>
  <RouterView v-if="bare" />
  <div v-else class="app-shell">
    <aside class="sidebar">
      <div class="brand">
        <Activity :size="22" :stroke-width="2.25" class="brand-icon" />
        <div class="brand-text">
          <h1>frappe-monitor</h1>
          <span class="brand-sub">production</span>
        </div>
      </div>
      <nav>
        <RouterLink to="/servers" class="nav-link">
          <Server :size="18" :stroke-width="2" />
          <span>Servers</span>
        </RouterLink>
        <RouterLink to="/benches" class="nav-link">
          <Boxes :size="18" :stroke-width="2" />
          <span>Benches</span>
        </RouterLink>
        <RouterLink to="/sites" class="nav-link">
          <Globe :size="18" :stroke-width="2" />
          <span>Sites</span>
        </RouterLink>
        <RouterLink to="/alerts" class="nav-link">
          <Bell :size="18" :stroke-width="2" />
          <span>Alerts</span>
        </RouterLink>
      </nav>
      <button class="nav-link logout" type="button" @click="handleLogout">
        <LogOut :size="18" :stroke-width="2" />
        <span>Sign out</span>
      </button>
    </aside>
    <div class="main">
      <header class="header">
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
  grid-template-columns: 232px 1fr;
  min-height: 100vh;
}
.sidebar {
  background: var(--sidebar-bg);
  color: var(--sidebar-fg);
  padding: 1.25rem 0.75rem;
  border-right: 1px solid var(--card-border);
  display: flex;
  flex-direction: column;
  position: sticky;
  top: 0;
  height: 100vh;
}
.brand {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  padding: 0 0.6rem 1.5rem 0.6rem;
  border-bottom: 1px solid var(--card-border);
  margin-bottom: 1rem;
}
.brand-icon {
  color: var(--accent);
}
.brand-text h1 {
  font-size: 1rem;
  margin: 0;
  font-weight: 600;
  letter-spacing: -0.01em;
}
.brand-sub {
  font-size: 0.7rem;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.08em;
}
nav {
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
}
.nav-link {
  display: flex;
  align-items: center;
  gap: 0.7rem;
  padding: 0.55rem 0.7rem;
  border-radius: 6px;
  color: var(--muted);
  text-decoration: none;
  font-size: 0.92rem;
  font-weight: 500;
  transition: background 120ms ease, color 120ms ease;
}
.nav-link:hover {
  background: var(--bg-hover);
  color: var(--fg);
}
.nav-link.router-link-active {
  background: var(--sidebar-active);
  color: var(--accent-strong);
}
.logout {
  margin-top: auto;        /* shove to the bottom of the sidebar */
  background: transparent;
  border: 1px solid var(--card-border);
  cursor: pointer;
  width: 100%;
  text-align: left;
}
.logout:hover {
  border-color: var(--accent);
  color: var(--fg);
}
.main {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
}
.header {
  padding: 0.75rem 2rem;
  border-bottom: 1px solid var(--card-border);
  background: var(--bg);
  position: sticky;
  top: 0;
  z-index: 10;
}
.content {
  padding: 1.75rem 2rem;
  flex: 1;
}

/* ----- Tablet (≤ 1024px) ------------------------------------------ */
@media (max-width: 1024px) {
  .app-shell { grid-template-columns: 200px 1fr; }
  .content   { padding: 1.25rem 1.25rem; }
  .header    { padding: 0.75rem 1.25rem; }
}

/* ----- Mobile (≤ 720px): sidebar collapses to a sticky top bar ----- */
@media (max-width: 720px) {
  .app-shell {
    grid-template-columns: 1fr;
    grid-template-rows: auto auto 1fr;
  }
  .sidebar {
    position: sticky;
    top: 0;
    height: auto;
    z-index: 20;
    border-right: none;
    border-bottom: 1px solid var(--card-border);
    padding: 0.75rem 1rem;
    flex-direction: row;
    align-items: center;
    gap: 0.75rem;
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
  }
  .brand {
    border-bottom: none;
    padding: 0;
    margin-bottom: 0;
    flex-shrink: 0;
  }
  .brand-text h1 { font-size: 0.92rem; }
  .brand-sub { display: none; }
  nav {
    flex-direction: row;
    gap: 0.25rem;
    flex: 1;
    justify-content: flex-end;
  }
  .nav-link {
    padding: 0.4rem 0.55rem;
    font-size: 0.85rem;
    flex-shrink: 0;
  }
  .nav-link span { display: none; }   /* icons only on mobile */
  .nav-link.router-link-active {
    background: var(--accent-soft);
  }
  .header { padding: 0.5rem 1rem; position: static; }
  .content { padding: 1rem; }
}
</style>
