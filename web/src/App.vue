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
import { realtime } from './realtime'

const route = useRoute()
const bare = computed(() => route.meta.layout === 'bare')

async function handleLogout() {
  // Confirm so a misclick on a phone (where the button sits next to
  // nav links) doesn't drop the operator out of the dashboard mid-task.
  if (!window.confirm('Sign out of frappe-monitor?')) return
  // Stop the live socket so it doesn't reconnect against a now-invalid
  // session during the brief window before the hard navigation lands.
  realtime.disconnect()
  try {
    await logout()
  } catch {
    // Server-side cookie-clear failed; we still tear down the local
    // view and route to /login. Login.vue's ?signed_out=1 branch
    // suppresses the auto-redirect so a stale-but-valid cookie can't
    // bounce the user back into the dashboard.
  }
  // Hard navigation so SPA state (cached fetch promises, in-flight
  // refresh timers) is dropped; routing inside the SPA would leave
  // those alive.
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
  /* Defense against any descendant (chart canvas, long monospace
     hostname, oversized table) bleeding past the viewport on phones.
     `clip` is the modern variant of `hidden` that doesn't establish
     a containing block for sticky/fixed elements, so the sticky
     sidebar + sticky header still work. Falls back to `hidden`
     anywhere `clip` isn't supported yet. */
  overflow-x: hidden;
  overflow-x: clip;
  /* Cap the shell at the viewport width so nothing horizontally
     escapes into "empty space to the right" territory. */
  max-width: 100vw;
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
  /* Without min-width:0 a flex/grid child whose content (echarts canvas,
     long monospace hostname) is wider than the column still claims its
     intrinsic width, pushing the whole page horizontally on phones.
     Forcing 0 lets the content scroll inside the column instead. */
  min-width: 0;
  overflow-x: hidden;
  overflow-x: clip;
}
.content {
  /* Same shrink guard for the inner content column. Per-view tables
     opt back into horizontal-scroll where they need it (they wrap a
     scrollable <table>, not the whole page). */
  min-width: 0;
  max-width: 100%;
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
    padding: 0.6rem 0.85rem;
    flex-direction: row;
    align-items: center;
    gap: 0.6rem;
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
    /* Comfortable thumb-size targets — anything smaller than ~40px
       wide is finicky on a phone and people fat-finger Sign-out
       when reaching for Servers. */
    padding: 0.5rem 0.65rem;
    min-width: 40px;
    justify-content: center;
    font-size: 0.85rem;
    flex-shrink: 0;
  }
  .nav-link span { display: none; }   /* icons only on mobile */
  .nav-link.router-link-active {
    background: var(--accent-soft);
  }
  /* Sign-out on mobile: same size as the other nav icons. The
     desktop styles set width:100% + text-align:left for the
     bottom-of-sidebar pinned button — both have to be unset here
     or the button claims the whole horizontal flex row and looks
     comically huge next to the other icons. */
  .logout {
    margin-top: 0;
    margin-left: 0.35rem;
    padding: 0.45rem 0.55rem;
    width: auto;
    text-align: center;
    border: 1px solid var(--card-border);
    border-radius: 6px;
    flex-shrink: 0;
  }
  .logout:hover {
    background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
    color: var(--status-unreachable);
    border-color: color-mix(in srgb, var(--status-unreachable) 50%, var(--card-border));
  }
  .header { padding: 0.5rem 1rem; position: static; }
  .content { padding: 1rem; }
}

/* ----- Phone (≤ 480px): sidebar wraps to two rows so brand + nav
        don't horizontally scroll on narrow handsets. ------------- */
@media (max-width: 480px) {
  .sidebar {
    flex-wrap: wrap;
    overflow-x: visible;
    /* Box-sizing belt-and-braces: padding shouldn't bleed past 100vw. */
    max-width: 100%;
  }
  .brand {
    flex: 1 1 100%;
    justify-content: flex-start;
    padding-bottom: 0.4rem;
    margin-bottom: 0.35rem;
    border-bottom: 1px solid var(--card-border);
    min-width: 0;
  }
  .brand-text { min-width: 0; overflow: hidden; }
  .brand-text h1 {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  nav {
    flex: 1 1 0;
    min-width: 0;
    justify-content: flex-start;
    gap: 0.15rem;
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
    scrollbar-width: none;
  }
  nav::-webkit-scrollbar { display: none; }
  .nav-link {
    /* Tighter taps on a 360px wide phone so 4 routes + sign-out
       fit. Still ≥40px wide for thumb comfort. */
    min-width: 40px;
    padding: 0.45rem 0.55rem;
  }
  .logout {
    margin-left: 0.35rem;
    padding: 0.45rem 0.55rem;
    flex-shrink: 0;
  }
}
</style>
