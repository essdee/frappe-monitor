import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// Login.vue's redirectNext() is the open-redirect sanitizer: it must only
// follow same-origin absolute PATHS ("/foo") and reject protocol-relative or
// scheme URLs ("//evil.com", "http://evil.com") by falling back to "/". We
// exercise the REAL component (not a re-implementation): mount it, make
// whoami() report "already authed" so onMounted calls redirectNext with the
// ?next param, and assert what router.replace receives.

// --- router mock (controllable route + spy router) ---
let routeQuery: Record<string, any> = {}
const replaceMock = vi.fn()
vi.mock('vue-router', () => ({
  useRoute: () => ({ get query() { return routeQuery } }),
  useRouter: () => ({ replace: replaceMock, push: vi.fn() }),
}))

// --- api mock: control whoami() / login() ---
const whoamiMock = vi.fn(async () => true)
const loginMock = vi.fn(async (_pw: string) => {})
vi.mock('../api', () => ({
  whoami: () => whoamiMock(),
  login: (pw: string) => loginMock(pw),
}))

// lucide icons render as components; stub them to avoid noise.
vi.mock('lucide-vue-next', () => ({
  Activity: { render: () => null },
  Lock: { render: () => null },
  AlertCircle: { render: () => null },
  CheckCircle2: { render: () => null },
}))

import Login from '../views/Login.vue'

async function mountWithNext(next: string | undefined) {
  routeQuery = next === undefined ? {} : { next }
  whoamiMock.mockResolvedValue(true) // already authed → bounce on mount
  const wrapper = mount(Login)
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  routeQuery = {}
  replaceMock.mockClear()
  whoamiMock.mockReset()
  loginMock.mockReset()
})

describe('Login open-redirect sanitizer (redirectNext)', () => {
  it('follows a safe same-origin path', async () => {
    await mountWithNext('/servers')
    expect(replaceMock).toHaveBeenCalledTimes(1)
    expect(replaceMock).toHaveBeenCalledWith('/servers')
  })

  it('rejects protocol-relative "//evil.com" → falls back to "/"', async () => {
    await mountWithNext('//evil.com')
    expect(replaceMock).toHaveBeenCalledWith('/')
  })

  it('rejects an absolute scheme URL "http://evil.com" → "/"', async () => {
    await mountWithNext('http://evil.com')
    expect(replaceMock).toHaveBeenCalledWith('/')
  })

  it('rejects "https://evil.com/path" → "/"', async () => {
    await mountWithNext('https://evil.com/path')
    expect(replaceMock).toHaveBeenCalledWith('/')
  })

  it('rejects a relative path lacking a leading slash → "/"', async () => {
    await mountWithNext('evil')
    expect(replaceMock).toHaveBeenCalledWith('/')
  })

  it('defaults to "/" when no next param is present', async () => {
    await mountWithNext(undefined)
    expect(replaceMock).toHaveBeenCalledWith('/')
  })

  it('preserves a deeper safe path with query string', async () => {
    await mountWithNext('/servers/3?tab=metrics')
    expect(replaceMock).toHaveBeenCalledWith('/servers/3?tab=metrics')
  })

  it('does NOT bounce on mount when ?signed_out=1 is set', async () => {
    routeQuery = { signed_out: '1', next: '/servers' }
    whoamiMock.mockResolvedValue(true)
    mount(Login)
    await flushPromises()
    // justSignedOut short-circuits onMounted before whoami/redirect.
    expect(whoamiMock).not.toHaveBeenCalled()
    expect(replaceMock).not.toHaveBeenCalled()
  })
})

describe('Login submit flow', () => {
  it('after a successful login, redirects via the sanitized next', async () => {
    routeQuery = { next: '/alerts' }
    whoamiMock.mockResolvedValue(false) // not authed → show form
    loginMock.mockResolvedValue(undefined)
    const wrapper = mount(Login)
    await flushPromises()

    await wrapper.find('input[type="password"]').setValue('hunter2')
    await wrapper.find('form').trigger('submit.prevent')
    await flushPromises()

    expect(loginMock).toHaveBeenCalledWith('hunter2')
    expect(replaceMock).toHaveBeenCalledWith('/alerts')
  })

  it('a malicious next is still sanitized on the post-login redirect', async () => {
    routeQuery = { next: '//evil.com' }
    whoamiMock.mockResolvedValue(false)
    loginMock.mockResolvedValue(undefined)
    const wrapper = mount(Login)
    await flushPromises()

    await wrapper.find('input[type="password"]').setValue('pw')
    await wrapper.find('form').trigger('submit.prevent')
    await flushPromises()

    expect(replaceMock).toHaveBeenCalledWith('/')
  })

  it('surfaces a login error and does not redirect', async () => {
    routeQuery = {}
    whoamiMock.mockResolvedValue(false)
    loginMock.mockRejectedValue(new Error('Wrong password'))
    const wrapper = mount(Login)
    await flushPromises()

    await wrapper.find('input[type="password"]').setValue('bad')
    await wrapper.find('form').trigger('submit.prevent')
    await flushPromises()

    expect(replaceMock).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('Wrong password')
  })
})
