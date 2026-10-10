<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import {
  Activity,
  Bell,
  Boxes,
  Globe,
	FileSearch,
  LayoutDashboard,
  LogOut,
  Server,
  Settings,
  ShieldCheck,
} from 'lucide-vue-next'
import TimelineFilter from './components/TimelineFilter.vue'
import { logout } from './api'
import { authEnabled, markAuthenticated } from './authState'
import { displayTimezone } from './timeDisplay'

const route = useRoute()
const bare = computed(() => route.meta.layout === 'bare')
const pageTitle = computed(() => String(route.meta.title ?? 'Overview'))
const logoutError = ref('')

async function handleLogout() {
  if (!window.confirm('Sign out of Frappe Monitor?')) return
  logoutError.value = ''
  try {
    await logout()
		markAuthenticated(false)
    window.location.assign('/login?signed_out=1')
  } catch (e) {
    logoutError.value = e instanceof Error ? e.message : String(e)
  }
}
</script>

<template>
  <RouterView v-if="bare" />
  <div v-else class="app-shell">
    <aside class="sidebar">
      <RouterLink to="/" class="brand" aria-label="Frappe Monitor overview">
        <span class="brand-mark"><Activity :size="21" :stroke-width="2.4" /></span>
        <span class="brand-copy">
          <strong>Frappe Monitor</strong>
          <small>Read-only observability</small>
        </span>
      </RouterLink>

      <nav aria-label="Primary navigation">
        <span class="nav-group">Monitor</span>
        <RouterLink to="/" class="nav-link" exact-active-class="router-link-active">
          <LayoutDashboard :size="18" />
          <span>Overview</span>
        </RouterLink>
        <RouterLink to="/servers" class="nav-link">
          <Server :size="18" />
          <span>Servers</span>
        </RouterLink>
        <span class="nav-group workloads">Workloads</span>
        <RouterLink to="/benches" class="nav-link">
          <Boxes :size="18" />
          <span>Benches</span>
        </RouterLink>
        <RouterLink to="/sites" class="nav-link">
          <Globe :size="18" />
          <span>Sites</span>
        </RouterLink>
				<RouterLink to="/logs" class="nav-link">
					<FileSearch :size="18" />
					<span>Logs</span>
				</RouterLink>
        <RouterLink to="/alerts" class="nav-link">
          <Bell :size="18" />
          <span>Incidents</span>
        </RouterLink>
        <span class="nav-group workloads">Administration</span>
        <RouterLink to="/settings" class="nav-link">
          <Settings :size="18" />
          <span>Settings</span>
        </RouterLink>
      </nav>

      <div class="sidebar-footer">
        <div class="control-plane">
          <ShieldCheck :size="16" />
          <span><strong>SSH telemetry</strong><small>Read-only · {{ displayTimezone }}</small></span>
        </div>
        <button v-if="authEnabled" class="nav-link logout" type="button" @click="handleLogout">
          <LogOut :size="17" />
          <span>Sign out</span>
        </button>
        <p v-if="logoutError" class="logout-error">{{ logoutError }}</p>
      </div>
    </aside>

    <div class="main-column">
      <header class="topbar">
        <div class="current-page">
          <span>Monitor</span>
          <strong>{{ pageTitle }}</strong>
        </div>
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
  grid-template-columns: 248px minmax(0, 1fr);
  min-height: 100vh;
}

.sidebar {
  position: sticky;
  top: 0;
  height: 100vh;
  padding: 1rem;
  background: color-mix(in srgb, var(--sidebar-bg) 94%, transparent);
  border-right: 1px solid var(--card-border);
  display: flex;
  flex-direction: column;
  z-index: 30;
}

.brand {
  display: flex;
  align-items: center;
  gap: 0.7rem;
  color: var(--fg-strong);
  padding: 0.45rem 0.45rem 1.2rem;
  margin-bottom: 0.85rem;
  border-bottom: 1px solid var(--card-border);
}
.brand:hover { color: var(--fg-strong); }
.brand-mark {
  width: 38px;
  height: 38px;
  border-radius: 11px;
  display: grid;
  place-items: center;
  color: white;
  background: linear-gradient(145deg, var(--accent-strong), var(--accent));
  box-shadow: 0 8px 26px color-mix(in srgb, var(--accent) 28%, transparent);
}
.brand-copy { display: flex; flex-direction: column; min-width: 0; }
.brand-copy strong { font-size: 0.93rem; letter-spacing: -0.015em; }
.brand-copy small { color: var(--muted); font-size: 0.69rem; margin-top: 0.1rem; }

nav { display: flex; flex-direction: column; gap: 0.2rem; }
.nav-group {
  padding: 0.7rem 0.65rem 0.35rem;
  color: var(--muted-2);
  font-size: 0.66rem;
  font-weight: 700;
  letter-spacing: 0.12em;
  text-transform: uppercase;
}
.nav-group.workloads { margin-top: 0.35rem; }
.nav-link {
  min-height: 42px;
  display: flex;
  align-items: center;
  gap: 0.7rem;
  padding: 0.55rem 0.7rem;
  border-radius: 9px;
  color: var(--muted);
  font-size: 0.9rem;
  font-weight: 560;
  transition: color 140ms ease, background 140ms ease, transform 140ms ease;
}
.nav-link:hover { color: var(--fg); background: var(--bg-hover); }
.nav-link.router-link-active {
  color: var(--fg-strong);
  background: linear-gradient(90deg, var(--accent-soft), color-mix(in srgb, var(--accent-soft) 42%, transparent));
  box-shadow: inset 2px 0 0 var(--accent);
}
.sidebar-footer { margin-top: auto; }
.control-plane {
  display: flex;
  align-items: center;
  gap: 0.55rem;
  padding: 0.7rem;
  margin-bottom: 0.55rem;
  color: var(--status-reachable);
  border: 1px solid color-mix(in srgb, var(--status-reachable) 18%, var(--card-border));
  background: color-mix(in srgb, var(--status-reachable) 5%, var(--card-bg));
  border-radius: 10px;
}
.control-plane span { display: flex; flex-direction: column; }
.control-plane strong { color: var(--fg); font-size: 0.77rem; }
.control-plane small { font-size: 0.68rem; margin-top: 0.05rem; }
.logout { width: 100%; border: 0; background: transparent; cursor: pointer; text-align: left; }
.logout-error { color: var(--status-unreachable); font-size: 0.72rem; padding: 0 0.65rem; }

.main-column { min-width: 0; min-height: 100vh; }
.topbar {
  min-height: 72px;
  padding: 0.8rem clamp(1rem, 2.4vw, 2.5rem);
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1.5rem;
  position: sticky;
  top: 0;
  z-index: 20;
  border-bottom: 1px solid var(--card-border);
  background: color-mix(in srgb, var(--bg) 88%, transparent);
  backdrop-filter: blur(16px);
}
.current-page { display: flex; flex-direction: column; flex: 0 0 auto; }
.current-page span { color: var(--muted-2); font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.1em; }
.current-page strong { font-size: 0.93rem; margin-top: 0.08rem; }
.content {
  width: 100%;
  max-width: 1680px;
  margin: 0 auto;
  padding: clamp(1.25rem, 2.5vw, 2.5rem);
}

@media (max-width: 940px) {
  .app-shell { grid-template-columns: 210px minmax(0, 1fr); }
  .topbar { align-items: flex-start; flex-direction: column; gap: 0.5rem; position: static; }
  .current-page { display: none; }
}

@media (max-width: 720px) {
  .app-shell { display: block; padding-bottom: 68px; }
  .sidebar {
    position: fixed;
    inset: auto 0 0 0;
    width: 100%;
    height: 68px;
    padding: 0.45rem 0.5rem max(0.45rem, env(safe-area-inset-bottom));
    border: 0;
    border-top: 1px solid var(--card-border);
    background: color-mix(in srgb, var(--sidebar-bg) 94%, transparent);
    backdrop-filter: blur(18px);
  }
  .brand, .nav-group, .sidebar-footer { display: none; }
  nav { display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); align-items: center; height: 100%; }
  .nav-link {
    min-width: 0;
    min-height: 50px;
    padding: 0.4rem 0.35rem;
    flex-direction: column;
    justify-content: center;
    gap: 0.15rem;
    font-size: 0.61rem;
  }
  .nav-link.router-link-active { box-shadow: inset 0 2px 0 var(--accent); }
  .topbar { min-height: auto; padding: 0.65rem 0.85rem; }
  .content { padding: 1rem 0.85rem 1.5rem; }
}
</style>
