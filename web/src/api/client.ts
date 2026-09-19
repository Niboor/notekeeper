import createClient from 'openapi-fetch'
import type { components, paths } from './user.schema'
import { CLIENT_HEADER, refreshSession } from './session'

export type Schemas = components['schemas']

/** Endpoints under /auth answer for themselves; a 401 there must not trigger a renewal. */
function isAuthEndpoint(url: string): boolean {
  return new URL(url, window.location.origin).pathname.startsWith('/api/v1/auth/')
}

/**
 * fetch with silent renewal: a 401 on an ordinary call renews the session once and retries the
 * request. When the renewal is refused the 401 is returned and the auth layer shows the login page.
 */
export async function apiFetch(request: Request): Promise<Response> {
  const retry = request.clone()
  const res = await fetch(request)
  if (res.status !== 401 || isAuthEndpoint(request.url)) return res
  if (!(await refreshSession())) return res
  return fetch(retry)
}

export const api = createClient<paths>({ baseUrl: window.location.origin, headers: CLIENT_HEADER, fetch: apiFetch })

/** A problem+json error from the API. */
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message?: string,
  ) {
    super(message ?? code)
  }
}

/** Throws an ApiError for a failed openapi-fetch result, otherwise returns the data. */
export function unwrap<T>(result: { data?: T; error?: unknown; response: Response }): T {
  if (result.error !== undefined || result.data === undefined) {
    const p = (result.error ?? {}) as { code?: string; detail?: string }
    throw new ApiError(result.response.status, p.code ?? 'error', p.detail)
  }
  return result.data
}

/** For endpoints that answer 204: throws on failure. */
export function unwrapEmpty(result: { error?: unknown; response: Response }): void {
  if (!result.response.ok) {
    const p = (result.error ?? {}) as { code?: string; detail?: string }
    throw new ApiError(result.response.status, p.code ?? 'error', p.detail)
  }
}
