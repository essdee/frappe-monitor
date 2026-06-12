import { test, expect, type Page } from '@playwright/test'

const PASSWORD = 'e2e-password'

async function login(page: Page) {
  await page.goto('/')
  const pw = page.locator('input[type=password]')
  await expect(pw).toBeVisible()
  await pw.fill(PASSWORD)
  await page.getByRole('button', { name: /sign in/i }).click()
  await expect(page.getByRole('heading', { name: /Servers/ })).toBeVisible()
}

test('login rejects the wrong password and accepts the right one', async ({ page }) => {
  await page.goto('/')
  const pw = page.locator('input[type=password]')
  await expect(pw).toBeVisible()

  await pw.fill('definitely-wrong')
  await page.getByRole('button', { name: /sign in/i }).click()
  await expect(page.locator('.error')).toBeVisible()
  await expect(page.getByRole('button', { name: /sign in/i })).toBeVisible() // still on login

  await pw.fill(PASSWORD)
  await page.getByRole('button', { name: /sign in/i }).click()
  await expect(page.getByRole('heading', { name: /Servers/ })).toBeVisible()
  await page.screenshot({ path: 'screenshots/01-logged-in.png', fullPage: true })
})

test('every nav section loads', async ({ page }) => {
  await login(page)
  const sections: Array<[string, RegExp]> = [
    ['Servers', /Servers/],
    ['Benches', /Benches/],
    ['Sites', /Sites/],
    ['Databases', /Databases/],
    ['Control', /Control panel/],
    ['Alerts', /Alerts/],
  ]
  for (const [label, heading] of sections) {
    await page.getByRole('link', { name: label, exact: true }).click()
    await expect(page.getByRole('heading', { name: heading })).toBeVisible()
    await page.screenshot({ path: `screenshots/nav-${label}.png`, fullPage: true })
  }
})

test('a server can be added and appears in the list', async ({ page }) => {
  await login(page)
  await page.getByRole('button', { name: 'Add server' }).click()
  await page.getByPlaceholder('prod1').fill('e2e-server')
  await page.getByPlaceholder('bench1.example.com').fill('e2e.example.com')
  await page.getByPlaceholder('frappe', { exact: true }).fill('ubuntu')
  await page.getByPlaceholder(/id_ed25519/).fill('/tmp/e2e-key')
  await page.locator('form').getByRole('button', { name: 'Add server' }).click()
  await expect(page.getByText('e2e-server')).toBeVisible()
  await page.screenshot({ path: 'screenshots/02-server-added.png', fullPage: true })
})

test('control panel loads its allowlisted action catalog', async ({ page }) => {
  await login(page)
  // The run form only renders when a server exists; create one (authenticated
  // via the cookie login set on this context).
  await page.request.post('/api/v1/servers', {
    data: { name: 'ctl-srv', hostname: 'ctl.example.com', ssh_user: 'ubuntu', ssh_port: 22, ssh_key_path: '/tmp/k' },
  })
  await page.getByRole('link', { name: 'Control', exact: true }).click()
  await expect(page.getByRole('heading', { name: /Control panel/ })).toBeVisible()
  // The action dropdown is populated from /control/actions — a known
  // allowlisted action must be present.
  await expect(page.locator('option', { hasText: 'Migrate site' })).toHaveCount(1)
  await page.screenshot({ path: 'screenshots/03-control-panel.png', fullPage: true })
})
