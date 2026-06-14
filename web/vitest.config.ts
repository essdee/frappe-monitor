import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// vitest config — jsdom so tests can touch window/location/WebSocket and
// mount Vue components. Kept separate from vite.config.ts (which writes
// the production build into ../internal/web/dist) so the test runner
// doesn't inherit the embed-targeting build options.
export default defineConfig({
  plugins: [vue()],
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/__tests__/**/*.{test,spec}.ts', 'src/**/*.{test,spec}.ts'],
  },
})
