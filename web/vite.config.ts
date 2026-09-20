/// <reference types="vitest/config" />
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The dev server proxies the API to Core so cookies stay same-origin (SEC-API-6).
const apiTarget = process.env.NK_API_PROXY ?? 'http://localhost:8080'
// The public share API lives on its own listener; the share page reaches it through the same proxy.
const publicTarget = process.env.NK_PUBLIC_PROXY ?? 'http://localhost:8082'

// Core checks the Host header of each listener, so the proxy must pass the browser's host on unchanged
// (the share page arrives on its own hostname).
const keepHost = (target: string) => ({
  target,
  changeOrigin: false,
  configure: (proxy: { on: (ev: 'proxyReq', fn: (req: { setHeader: (k: string, v: string) => void }, incoming: { headers: { host?: string } }) => void) => void }) =>
    proxy.on('proxyReq', (req, incoming) => {
      if (incoming.headers.host) req.setHeader('host', incoming.headers.host)
    }),
})
const proxy = { '^/api/public/': keepHost(publicTarget), '^/api/(?!public/)': keepHost(apiTarget) }

// Every build gets its own service worker cache (the page registers /sw.js?v=<id>): a release starts with
// an empty cache, and the previous release's hashed files are deleted when the new worker activates, so the
// cache does not grow with every version. Set NK_BUILD_ID (for instance to the git commit) for a
// reproducible id.
const buildId = process.env.NK_BUILD_ID ?? String(Date.now())

export default defineConfig({
  define: { __BUILD_ID__: JSON.stringify(buildId) },
  plugins: [react()],
  server: {
    proxy,
    allowedHosts: ['.localhost'],
  },
  // `vite preview` serves the production build; the end-to-end tests run against it.
  preview: {
    proxy,
    allowedHosts: ['.localhost'],
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
  },
})
