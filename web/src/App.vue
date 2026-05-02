<script setup lang="ts">
import { RouterView, RouterLink } from 'vue-router'
import {
  Server,
  Boxes,
  Globe,
  Bell,
  Activity,
} from 'lucide-vue-next'
import TimelineFilter from './components/TimelineFilter.vue'
</script>

<template>
  <div class="app-shell">
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
</style>
