import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The web client is fully decoupled from the Go daemon — it talks to the
// token-gated /v1 API over HTTP (CORS is enabled server-side), so the dev
// server and the API can run on different ports/hosts.
export default defineConfig({
  plugins: [vue()],
  server: { port: 5173 },
})
