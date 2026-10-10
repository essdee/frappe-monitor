import { afterEach, describe, expect, it, vi } from 'vitest'
import { logout, queryLabelMatchers, queryStringLiteral, whoami } from './api'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('query label escaping', () => {
  it('escapes quotes, backslashes, and control characters', () => {
    expect(queryStringLiteral('prod"\\\nsite')).toBe('"prod\\"\\\\\\nsite"')
  })

  it('builds exact PromQL and LogQL matchers from operator input', () => {
    expect(queryLabelMatchers({
      server: 'prod"east',
      bench: 'bench\\one',
    })).toBe('server="prod\\"east",bench="bench\\\\one"')
  })
})

describe('authentication API', () => {
  it('reports an unauthenticated protected server on 401', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 401 })))

    await expect(whoami()).resolves.toEqual({
      authenticated: false,
      auth_enabled: true,
      timezone: 'UTC',
    })
  })

  it('preserves the server authentication mode', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({
      authenticated: true,
      auth_enabled: false,
    })))

    await expect(whoami()).resolves.toEqual({
      authenticated: true,
      auth_enabled: false,
    })
  })

  it('does not pretend a failed logout succeeded', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('failed', {
      status: 500,
      statusText: 'Internal Server Error',
    })))

    await expect(logout()).rejects.toThrow('failed')
  })

  it('shows the API error message instead of a raw JSON dictionary', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({
      error: 'Telegram bot token is not configured',
    }, { status: 400 })))

    await expect(logout()).rejects.toThrow('Telegram bot token is not configured')
  })
})
