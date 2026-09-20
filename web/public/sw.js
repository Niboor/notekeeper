// The service worker keeps the application shell (page, scripts, styles, fonts) so the app opens
// without a network and starts fast (WEB-N8). It never stores anything from /api/: notes, files and
// tokens must not end up in a cache that outlives the session (SEC-DATA-1, SEC-CNT-3). Navigations use
// the network first and fall back to the stored shell; hashed assets are cache-first.
const SHELL = 'nk-shell-v1'

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(SHELL).then((c) => c.addAll(['/'])).then(() => self.skipWaiting()))
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((names) => Promise.all(names.filter((n) => n !== SHELL).map((n) => caches.delete(n))))
      .then(() => self.clients.claim()),
  )
})

self.addEventListener('fetch', (event) => {
  const req = event.request
  const url = new URL(req.url)
  if (req.method !== 'GET' || url.origin !== self.location.origin || url.pathname.startsWith('/api/')) return // the network, always
  if (req.mode === 'navigate') {
    event.respondWith(
      fetch(req)
        .then((res) => {
          const copy = res.clone()
          if (res.ok && url.pathname !== '/s') void caches.open(SHELL).then((c) => c.put('/', copy)) // the share page is never stored
          return res
        })
        .catch(() => caches.match('/').then((r) => r ?? Response.error())),
    )
    return
  }
  if (url.pathname.startsWith('/assets/') || /\.(woff2?|png|svg|webmanifest)$/.test(url.pathname)) {
    event.respondWith(
      caches.match(req).then(
        (hit) =>
          hit ??
          fetch(req).then((res) => {
            const copy = res.clone()
            if (res.ok) void caches.open(SHELL).then((c) => c.put(req, copy))
            return res
          }),
      ),
    )
  }
})
