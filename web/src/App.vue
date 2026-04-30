<script setup lang="ts">
import { RouterView, RouterLink } from 'vue-router'
</script>

<template>
  <div class="app-shell">
    <aside class="sidebar">
      <h1 class="brand">frappe-monitor</h1>
      <nav>
        <RouterLink to="/servers" class="nav-link">Servers</RouterLink>
      </nav>
    </aside>
    <main class="content">
      <RouterView />
    </main>
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
.nav-link {
  display: block;
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
  color: inherit;
  text-decoration: none;
}
.nav-link:hover,
.nav-link.router-link-active {
  background: var(--sidebar-active, #2d313a);
}
.content {
  padding: 1.5rem 2rem;
}
</style>
