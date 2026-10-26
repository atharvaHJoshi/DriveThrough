import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The Go API is the single source of truth; the dev server proxies to it so the
// browser sees one origin. That keeps cookies, CORS, and the WebSocket upgrade on
// the same scheme/host/port in development as in production, where the compiled
// assets are served by the Go binary itself.
const API_TARGET = process.env.VITE_API_TARGET ?? 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [react()],
  server: {
    port: Number(process.env.VITE_UI_PORT ?? 5173),
    // Bind on all interfaces so the dev server is reachable from a phone or a
    // second machine when testing the driver flow on real hardware.
    host: true,
    proxy: {
      '/api': {
        target: API_TARGET,
        changeOrigin: true,
        // /api/v1/ws/live is a WebSocket upgrade; without ws:true the proxy
        // forwards it as an ordinary request and the handshake never completes.
        ws: true,
      },
      '/health': { target: API_TARGET, changeOrigin: true },
      '/ready': { target: API_TARGET, changeOrigin: true },
    },
  },
  build: {
    // The Go server embeds this directory (internal/webui), so the build lands
    // inside the package rather than beside the frontend. `emptyOutDir` is
    // required because the target is outside the Vite root; the committed
    // placeholder index.html there is intentionally overwritten on every build.
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    sourcemap: true,
  },
})
