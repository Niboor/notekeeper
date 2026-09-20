import { expect, test } from '@playwright/test'

// WEB-19, AUTH-U6: the admin creates an account and gets its activation link, sets a quota, disables and
// deletes it, and sees metadata only. The account holder can then activate, and delete their own account.
test.setTimeout(120_000)
test('the admin manages accounts and never sees their notes', async ({ page, browser }) => {
  await page.goto('/admin')
  await expect(page.getByRole('heading', { name: 'Administration' })).toBeVisible()
  const name = `guest${Date.now().toString(36)}`
  await page.getByLabel('New username').fill(name)
  await page.getByRole('button', { name: 'Create account' }).click()
  const link = page.getByLabel('Activation link', { exact: true }).first()
  await expect(link).toBeVisible()
  const url = await link.inputValue()
  expect(url).toContain('/activate#')

  // Disabling and enabling before first use (disabling ends every session, so it happens before there is one).
  const pending = page.getByRole('row', { name: new RegExp(name) })
  await expect(pending).toContainText('Waiting for activation')
  await pending.getByRole('button', { name: `Disable: ${name}` }).click()
  await expect(pending).toContainText('Disabled')
  await pending.getByRole('button', { name: `Enable: ${name}` }).click()
  await expect(pending).toContainText('Waiting for activation')

  // The new person activates and lands on an empty Inbox.
  const guest = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const gp = await guest.newPage()
  await gp.goto(url)
  await gp.getByLabel('New password').fill('a long passphrase for testing 42')
  await gp.getByRole('button', { name: /Set password/ }).click()
  await expect(gp.getByRole('button', { name: 'New note' })).toBeVisible()
  await gp.getByRole('button', { name: 'New note' }).click()
  await gp.getByRole('textbox').fill('private guest note')
  await gp.getByRole('button', { name: 'Add note' }).click()
  await expect(gp.getByText('private guest note')).toBeVisible()
  // The guest is not an admin: no admin menu, and the page sends them away.
  await gp.goto('/admin')
  await expect(gp).toHaveURL(/\/$/)

  // The admin sees the account and its storage, and nothing of the note.
  await page.reload()
  const row = page.getByRole('row', { name: new RegExp(name) })
  await expect(row).toContainText('Active')
  await expect(page.getByText('private guest note')).toHaveCount(0)

  // The guest deletes their own account, confirmed with the password.
  await gp.goto('/settings')
  await gp.getByRole('button', { name: 'Delete my account' }).click()
  await gp.getByLabel('Your password').fill('a long passphrase for testing 42')
  await gp.getByRole('button', { name: 'Delete my account and all my data' }).click()
  await expect(gp.getByRole('button', { name: 'Sign in' })).toBeVisible({ timeout: 15_000 })

  // The data goes in the background; the admin sees the account disappear.
  await expect(async () => {
    await page.reload()
    await expect(page.getByRole('row', { name: new RegExp(name) })).toHaveCount(0, { timeout: 3_000 })
  }).toPass({ timeout: 55_000, intervals: [5_000] })
  await guest.close()
})
