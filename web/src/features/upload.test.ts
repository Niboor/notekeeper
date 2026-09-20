import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { uploadFile } from './hooks'

const file = (name: string) => new File(['x'], name, { type: 'text/plain' })
const ok = () => new Response(JSON.stringify({ id: 'a', filename: 'f', media_type: 'text/plain', size: 1 }), { status: 201 })

describe('uploadFile', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('runs at most three uploads at once, so choosing many files never trips the server limit (CR-051)', async () => {
    let running = 0
    let peak = 0
    vi.stubGlobal('fetch', async () => {
      running++
      peak = Math.max(peak, running)
      await new Promise((r) => setTimeout(r, 100))
      running--
      return ok()
    })
    const all = Promise.all(Array.from({ length: 8 }, (_, i) => uploadFile(file(`f${i}`))))
    await vi.advanceTimersByTimeAsync(1000)
    expect(await all).toHaveLength(8)
    expect(peak).toBe(3)
  })

  it('waits and tries again when the server says too many uploads, honouring Retry-After', async () => {
    const answers = [new Response('{"code":"too_many_uploads"}', { status: 429, headers: { 'Retry-After': '2' } }), ok()]
    const fetchMock = vi.fn(async () => answers.shift()!)
    vi.stubGlobal('fetch', fetchMock)
    const done = uploadFile(file('a'))
    await vi.advanceTimersByTimeAsync(1900)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(200)
    expect((await done).id).toBe('a')
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('reports a refusal that waiting cannot fix', async () => {
    vi.stubGlobal('fetch', async () => new Response('{"code":"too_large"}', { status: 413 }))
    await expect(uploadFile(file('big'))).rejects.toMatchObject({ code: 'too_large' })
  })
})
