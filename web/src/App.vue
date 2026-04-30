<script setup lang="ts">
import { RouterView, RouterLink } from 'vue-router'
import TimelineFilter from './components/TimelineFilter.vue'
</script>

<template>
  <div class="app-shell">
    <aside class="sidebar">
      <h1 class="brand">frappe-monitor</h1>
      <nav>
        <RouterLink to="/servers" class="nav-link">Servers</RouterLink>
        <RouterLink to="/benches" class="nav-link">Benches</RouterLink>
        <RouterLink to="/sites" class="nav-link">Sites</RouterLink>
        <span class="nav-disabled" title="Phase 6">Alerts</span>
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
  grid-template-columns: 220px 1fr;
  min-height: 100vh;
}
.sidebar {
  background: var(--sidebar-bg, #1a1d23);
  color: var(--sidebar-fg, #e8eaed);
  padding: 1.5rem 1rem;
}
.brand {
  font-size: 1.1rem;
  margin: 0 0 1.5rem 0;
}
.nav-link,
.nav-disabled {
  display: block;
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
  color: inherit;
  text-decoration: none;
  font-size: 0.95rem;
}
.nav-link:hover,
.nav-link.router-link-active {
  background: var(--sidebar-active, #2d313a);
}
.nav-disabled {
  color: #6b7280;
  cursor: not-allowed;
}
.main {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
}
.header {
  padding: 0.5rem 2rem;
  border-bottom: 1px solid var(--card-border);
  background: var(--card-bg);
}
.content {
  padding: 1.5rem 2rem;
  flex: 1;
}
</style>
