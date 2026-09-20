import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { createNote, openFreshBoard } from './helpers'

// WEB-N4: the main views have no serious accessibility violations, in the light and the dark theme.
for (const scheme of ['light', 'dark'] as const) {
  test(`main views pass axe in the ${scheme} theme`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme })
    const fx = await openFreshBoard(page)
    await createNote(page, 'a note with a [link](https://example.org)\n\n- [ ] and a task', fx.columns.Todo)
    for (const path of [`/p/${fx.pageId}`, '/trash', '/search?q=note', '/settings', '/reminders']) {
      await page.goto(path)
      await page.waitForLoadState('networkidle')
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
      const serious = results.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
      expect(serious.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`), `${path} (${scheme})`).toEqual([])
    }
  })
}
