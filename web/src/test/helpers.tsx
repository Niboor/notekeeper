import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import type { ReactElement } from 'react'
import { MemoryRouter } from 'react-router'
import { AuthProvider } from '../auth/AuthProvider'

export interface Route {
  method?: string
  path: string | RegExp
  status?: number
  body?: unknown
  /** Called with the request; may return a different response body. */
  handler?: (req: Request) => unknown
}

/** A fetch double: answers by method and path and records every request it saw. */
export function fakeFetch(routes: Route[]) {
  const calls: { method: string; path: string; body: string }[] = []
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(new URL(String(input), 'http://localhost'), init)
    const url = new URL(req.url)
    const method = req.method.toUpperCase()
    calls.push({ method, path: url.pathname + url.search, body: await req.clone().text() })
    const route = routes.find(
      (r) => (r.method ?? 'GET') === method && (typeof r.path === 'string' ? r.path === url.pathname : r.path.test(url.pathname)),
    )
    if (!route) return new Response(JSON.stringify({ code: 'not_found' }), { status: 404 })
    const body = route.handler ? route.handler(req) : route.body
    if (body instanceof Response) return body
    return new Response(body === undefined ? null : JSON.stringify(body), {
      status: route.status ?? 200,
      headers: { 'Content-Type': 'application/json' },
    })
  })
  return Object.assign(fn, { calls })
}

export function renderApp(ui: ReactElement, opts: { route?: string } = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 0 } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[opts.route ?? '/']}>
        <AuthProvider>{ui}</AuthProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

export const me = { id: 'u1', username: 'alice', display_name: 'Alice', is_admin: false, timezone: 'UTC', settings: {} }
