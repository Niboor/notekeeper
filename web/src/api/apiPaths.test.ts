import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

function sources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) sources(p, out)
    else if (/\.(ts|tsx)$/.test(name) && !/\.test\.tsx?$/.test(name) && !name.endsWith('.schema.ts')) out.push(p)
  }
  return out
}

// The web app talks only to the user API, and to the public share API on the share page, so the same
// functionality is open to a future native client (WEB-N2). Every address it uses is declared in the OpenAPI documents.
describe('what the web app calls', () => {
  const userSchema = readFileSync(join(__dirname, 'user.schema.ts'), 'utf8')
  const declared = new Set([...userSchema.matchAll(/^\s{4}"(\/api\/v1\/[^"]*)":/gm)].map((m) => m[1]!))
  const publicSchema = readFileSync(join(__dirname, 'public.schema.ts'), 'utf8')
  const publicDeclared = new Set([...publicSchema.matchAll(/^\s{4}"(\/api\/public\/v1\/[^"]*)":/gm)].map((m) => m[1]!))

  it('uses no address outside the user and public APIs', () => {
    expect(declared.size).toBeGreaterThan(40)
    const bad: string[] = []
    for (const file of sources(join(__dirname, '..'))) {
      const text = readFileSync(file, 'utf8')
      for (const m of text.matchAll(/['"`](\/(?:api|bot|metrics|healthz|readyz)[^'"`\s]*)['"`]/g)) {
        const url = m[1]!.split('?')[0]!
        if (url.endsWith('/') && [...declared].some((d) => d.startsWith(url))) continue // a prefix test, not a call
        const generic = url.replace(/\$\{[^}]*\}/g, '{id}')
        const known = [...declared].some((d) => d === url || d.replace(/\{[^}]+\}/g, '{id}') === generic) || [...publicDeclared].some((d) => d.replace(/\{[^}]+\}/g, '{id}') === generic) ||
          /^\/api\/v1\/(events)$/.test(url) // the event stream is not an operation with a body
        if (!known && !url.startsWith('/api/public/v1/share') && url !== '/api/v1/me/export') bad.push(`${file.split('/src/')[1]}: ${m[1]}`)
        if (url.startsWith('/bot')) bad.push(`${file}: ${m[1]}`)
      }
    }
    expect(bad).toEqual([])
  })
})
