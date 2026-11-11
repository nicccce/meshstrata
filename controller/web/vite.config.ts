import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': 'http://127.0.0.1:8443',
      '/healthz': 'http://127.0.0.1:8443',
      '/readyz': 'http://127.0.0.1:8443',
    },
  },
  build: {
    outDir: '../internal/webui/dist',
    // Preserve .gitkeep. The Make target removes previous generated output.
    emptyOutDir: false,
  },
})
