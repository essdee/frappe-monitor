import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Vite outputs to web/dist/. The Go binary embeds that directory via
// internal/web/embed.go. The dev server proxies /api/* to the running
// monitor binary (assumed at :8080) so client-side API calls work in
// dev without CORS gymnastics.
export default defineConfig({
  plugins: [vue()],
  build: {
    // Write directly into internal/web/dist so the Go //go:embed
    // directive in internal/web/embed.go resolves without symlinks
    // or copy steps.
    outDir: '../internal/web/dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: false,
        // Proxy the /api/v1/ws WebSocket upgrade too. changeOrigin stays
        // false so the Host header is preserved and the monitor's
        // same-origin check passes in dev.
        ws: true,
      },
      '/healthz': {
        target: 'http://localhost:8080',
        changeOrigin: false,
      },
    },
  },
})
