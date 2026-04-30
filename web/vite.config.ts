import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Vite outputs to web/dist/. The Go binary embeds that directory via
// internal/web/embed.go. The dev server proxies /api/* to the running
// monitor binary (assumed at :8080) so client-side API calls work in
// dev without CORS gymnastics.
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: false,
      },
      '/healthz': {
        target: 'http://localhost:8080',
        changeOrigin: false,
      },
    },
  },
})
