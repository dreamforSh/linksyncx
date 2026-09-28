import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'
import {
  clashCheckPlatformFor,
  clashErrorMessage,
  clashNodeLinkName,
  clashTrafficToday,
  clashTrafficTotal,
  clashTrafficTrend,
  countryFlagUrl,
  describeClashExitPause,
  estimateCodexImportCount,
  exitPlatformCheck,
  findClashExit,
  formatTrafficBytes,
  formatTrafficRate,
  isBlockingPlatformCheck,
  isClashExitPauseReason,
  isClashNodeLinkText,
  localizeClashUnavailableReason,
  stripClashExitPauseReason,
  summarizeClashNodeLinks
} from '../clash'
import type { ClashExitList, ClashNodeTraffic } from '@/types'

const t = (key: string, params?: Record<string, unknown>) => (params ? `${key} ${JSON.stringify(params)}` : key)

/** Resolves a dotted key in a locale tree; used to make sure dynamic keys exist. */
function lookup(messages: unknown, key: string): unknown {
  return key.split('.').reduce<unknown>((node, segment) => (node as Record<string, unknown> | undefined)?.[segment], messages)
}

describe('clash node traffic', () => {
  it('formats byte counts compactly and never shows negatives or NaN', () => {
    expect(formatTrafficBytes(0)).toBe('0 B')
    expect(formatTrafficBytes(-5)).toBe('0 B')
    expect(formatTrafficBytes(undefined)).toBe('0 B')
    expect(formatTrafficBytes(Number.NaN)).toBe('0 B')
    expect(formatTrafficBytes(512)).toBe('512 B')
    expect(formatTrafficBytes(1536)).toBe('1.5 KB')
    expect(formatTrafficBytes(1024 ** 3)).toBe('1 GB')
    expect(formatTrafficBytes(5 * 1024 ** 3 + 1024 ** 3 / 4)).toBe('5.25 GB')
    expect(formatTrafficBytes(3 * 1024 ** 6)).toBe('3072 PB')
    expect(formatTrafficRate(10 * 1024)).toBe('10 KB/s')
  })

  it('sums totals, today and the daily trend, tolerating older payloads', () => {
    const traffic: ClashNodeTraffic = {
      upload_bytes: 10,
      download_bytes: 90,
      today_upload_bytes: 1,
      today_download_bytes: 4,
      daily: [
        { date: '2026-09-26', upload_bytes: 2, download_bytes: 3 },
        { date: '2026-09-27', upload_bytes: 1, download_bytes: 4 }
      ],
      updated_at: null,
      upload_rate: 0,
      download_rate: 0,
      connections: 0
    }
    expect(clashTrafficTotal(traffic)).toBe(100)
    expect(clashTrafficToday(traffic)).toBe(5)
    expect(clashTrafficTrend(traffic)).toEqual([5, 5])
    expect(clashTrafficTotal(undefined)).toBe(0)
    expect(clashTrafficToday(undefined)).toBe(0)
    expect(clashTrafficTrend(undefined)).toEqual([])
  })
})

describe('clash pause reasons', () => {
  it('recognises the [clash-exit] prefix only', () => {
    expect(isClashExitPauseReason('[clash-exit] HK 01: health check failing')).toBe(true)
    expect(isClashExitPauseReason('  [clash-exit] x')).toBe(true)
    expect(isClashExitPauseReason('{"status_code":429}')).toBe(false)
    expect(isClashExitPauseReason(null)).toBe(false)
    expect(stripClashExitPauseReason('[clash-exit] HK 01: health check failing')).toBe('HK 01: health check failing')
  })

  it('splits node name and localized reason, even when the node name contains ": "', () => {
    expect(describeClashExitPause('[clash-exit] HK 01: health check failing', t)).toEqual({
      node: 'HK 01',
      reason: 'admin.clash.reasons.healthFailing'
    })
    expect(describeClashExitPause('[clash-exit] A: B: node invalid: unsupported cipher', t)).toEqual({
      node: 'A: B',
      reason: 'admin.clash.reasons.nodeInvalid {"detail":"unsupported cipher"}'
    })
    expect(describeClashExitPause('[clash-exit] SG 01: exit IP changed to 1.2.3.4, awaiting confirmation', t)).toEqual({
      node: 'SG 01',
      reason: 'admin.clash.reasons.exitChanged {"ip":"1.2.3.4"}'
    })
    expect(describeClashExitPause('[clash-exit] something new', t)).toEqual({ node: '', reason: 'something new' })
    expect(describeClashExitPause('manual pause', t)).toBeNull()
  })

  it('localizes every server unavailable reason and leaves unknown text untouched', () => {
    for (const [raw, key] of [
      ['subscription deleted', 'admin.clash.reasons.subscriptionDeleted'],
      ['subscription disabled', 'admin.clash.reasons.subscriptionDisabled'],
      ['node removed from subscription', 'admin.clash.reasons.nodeMissing'],
      ['node disabled', 'admin.clash.reasons.nodeDisabled'],
      ['health check failing', 'admin.clash.reasons.healthFailing'],
      ['node hidden', 'admin.clash.reasons.nodeHidden'],
      // Node status_reason vocabulary shown in the nodes table.
      ['hidden by admin', 'admin.clash.reasons.nodeHidden'],
      ['removed from subscription', 'admin.clash.reasons.nodeMissing'],
      ['disabled by admin', 'admin.clash.reasons.disabledByAdmin'],
      ['server is a loopback or link-local address', 'admin.clash.reasons.loopbackServer'],
      ['server is a private network address', 'admin.clash.reasons.privateServer'],
      ["exit IP equals this server's own IP", 'admin.clash.reasons.exitIsServer']
    ]) {
      expect(localizeClashUnavailableReason(raw, t)).toBe(key)
      expect(lookup(zh, key)).toEqual(expect.any(String))
      expect(lookup(en, key)).toEqual(expect.any(String))
    }
    expect(localizeClashUnavailableReason('exit IP changed to , awaiting confirmation', t)).toBe(
      'admin.clash.reasons.exitChanged {"ip":"-"}'
    )
    expect(localizeClashUnavailableReason('listener unavailable: dial tcp 127.0.0.1:20001: refused', t)).toBe(
      'admin.clash.reasons.listenerUnavailable {"detail":"dial tcp 127.0.0.1:20001: refused"}'
    )
    expect(lookup(zh, 'admin.clash.reasons.listenerUnavailable')).toEqual(expect.any(String))
    expect(localizeClashUnavailableReason('brand new reason', t)).toBe('brand new reason')
    expect(localizeClashUnavailableReason('', t)).toBe('')
  })
})

describe('clash platform checks', () => {
  it('maps account platforms to the relevant check', () => {
    expect(clashCheckPlatformFor('anthropic')).toBe('anthropic')
    expect(clashCheckPlatformFor('openai')).toBe('openai')
    expect(clashCheckPlatformFor('gemini')).toBe('gemini')
    expect(clashCheckPlatformFor('antigravity')).toBe('gemini')
    expect(clashCheckPlatformFor('grok')).toBe('grok')
    expect(clashCheckPlatformFor('kimi')).toBeNull()
    expect(clashCheckPlatformFor(undefined)).toBeNull()
  })

  it('only treats fail and challenge as blocking', () => {
    expect(isBlockingPlatformCheck('fail')).toBe(true)
    expect(isBlockingPlatformCheck('challenge')).toBe(true)
    expect(isBlockingPlatformCheck('warn')).toBe(false)
    expect(isBlockingPlatformCheck('pass')).toBe(false)
    expect(isBlockingPlatformCheck('')).toBe(false)
    const exit = { platform_checks: { checked_at: null, results: { gemini: 'challenge' as const } } }
    expect(exitPlatformCheck(exit, 'antigravity')).toBe('challenge')
    expect(exitPlatformCheck(exit, 'openai')).toBe('')
    expect(exitPlatformCheck(exit, 'kimi')).toBe('')
  })

  it('finds exits by proxy id and builds flag URLs only for ISO codes', () => {
    const list = { exits: [{ proxy_id: 5 }, { proxy_id: 6 }] } as unknown as ClashExitList
    expect(findClashExit(list, 6)).toEqual({ proxy_id: 6 })
    expect(findClashExit(list, 7)).toBeUndefined()
    expect(findClashExit(null, 6)).toBeUndefined()
    expect(countryFlagUrl('JP')).toBe('https://unpkg.com/flag-icons/flags/4x3/jp.svg')
    expect(countryFlagUrl('')).toBe('')
    expect(countryFlagUrl('../x')).toBe('')
  })
})

describe('clashErrorMessage', () => {
  const codes = [
    'CLASH_EXIT_OCCUPIED',
    'CLASH_EXIT_UNAVAILABLE',
    'CLASH_EXIT_UNPROBED',
    'CLASH_EXIT_RELAY_UNSUPPORTED',
    'CLASH_EXIT_BULK_UNSUPPORTED',
    'CLASH_MANAGED_PROXY_READONLY',
    'CLASH_PROXY_PORT_RESERVED',
    'CLASH_ENCRYPTION_KEY_REQUIRED',
    'CLASH_PROFILE_IN_USE',
    'CLASH_PROFILE_INVALID',
    'CLASH_PROFILE_NOT_FOUND',
    'CLASH_NODE_NOT_FOUND',
    'CLASH_FETCH_FAILED',
    'CLASH_POOL_DISABLED',
    'CLASH_RUNTIME_UNAVAILABLE',
    'CLASH_TOO_MANY_NODES'
  ]
  const keyOf = (message: string) => message.split(' ')[0]

  it.each(codes)('maps %s to a message present in both locales', (reason) => {
    const message = clashErrorMessage({ status: 400, reason, message: 'detail' }, t)
    expect(message).toBeTruthy()
    const key = keyOf(message!)
    expect(lookup(zh, key), `zh ${key}`).toEqual(expect.any(String))
    expect(lookup(en, key), `en ${key}`).toEqual(expect.any(String))
  })

  it('includes occupying account names from metadata', () => {
    const occupied = clashErrorMessage(
      { status: 409, reason: 'CLASH_EXIT_OCCUPIED', message: 'x', metadata: { accounts: 'acc-a, acc-b' } },
      t
    )
    expect(occupied).toBe('admin.clash.errors.exitOccupiedWithAccounts {"accounts":"acc-a, acc-b"}')
    expect(lookup(zh, 'admin.clash.errors.exitOccupiedWithAccounts')).toEqual(expect.any(String))
    expect(lookup(en, 'admin.clash.errors.profileInUseWithAccounts')).toEqual(expect.any(String))
    expect(clashErrorMessage({ reason: 'CLASH_PROFILE_IN_USE', metadata: { accounts: 'a' } }, t)).toBe(
      'admin.clash.errors.profileInUseWithAccounts {"accounts":"a"}'
    )
  })

  it('passes the server detail to fetch/validation errors', () => {
    expect(clashErrorMessage({ reason: 'CLASH_FETCH_FAILED', message: 'unexpected status 403' }, t)).toBe(
      'admin.clash.errors.fetchFailed {"message":"unexpected status 403"}'
    )
  })

  it('returns null for unrelated or unknown errors', () => {
    expect(clashErrorMessage({ reason: 'ACCOUNT_NOT_FOUND', message: 'x' }, t)).toBeNull()
    expect(clashErrorMessage({ reason: 'CLASH_SOMETHING_NEW', message: 'x' }, t)).toBeNull()
    expect(clashErrorMessage(new Error('network'), t)).toBeNull()
    expect(clashErrorMessage(undefined, t)).toBeNull()
  })
})

describe('estimateCodexImportCount', () => {
  it.each([
    ['', 0],
    ['   \n  ', 0],
    ['sk-single-token', 1],
    ['token-a\ntoken-b\n\ntoken-c', 3],
    ['{"tokens":{"access_token":"a"}}', 1],
    ['{\n  "tokens": {\n    "access_token": "a"\n  }\n}', 1],
    ['[{"a":1},{"b":2}]', 2],
    ['[[{"a":1}],[{"b":2},{"c":3}]]', 3],
    ['{"a":1}{"b":"}{"}', 2],
    ['{"a":1}\n{"b":2}', 2],
    ['{\n "a": 1\n}\n{\n "b": 2\n}', 2]
  ])('%j -> %i', (content, expected) => {
    expect(estimateCodexImportCount(content)).toBe(expected)
  })
})

describe('node share links', () => {
  // Made-up credentials in the SIP002 form providers hand out (base64url userinfo, obfs plugin).
  const ssLink =
    'ss://YWVzLTEyOC1nY206dGVzdC1wYXNzd29yZA@us.example.com:13277?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dcdn.example.com' +
    '#%F0%9F%87%BA%F0%9F%87%B8%20%E7%BE%8E%E5%9B%BD-%E6%B4%9B%E6%9D%89%E7%9F%B6%2004'
  const vmessLink = 'vmess://' + btoa(JSON.stringify({ v: '2', ps: 'JP 01', add: 'jp.example.com', port: '443', id: 'x' }))

  it('recognizes share links but not subscription URLs', () => {
    expect(isClashNodeLinkText(ssLink)).toBe(true)
    expect(isClashNodeLinkText(`\n  ${vmessLink}\n`)).toBe(true)
    expect(isClashNodeLinkText('hy2://pw@a.example.com:443#A')).toBe(true)
    expect(isClashNodeLinkText('hysteria2+realm://pw@a.example.com:443')).toBe(true)
    expect(isClashNodeLinkText('SOCKS5://u:p@a.example.com:1080')).toBe(true)
    expect(isClashNodeLinkText('https://sub.example.com/api?token=x')).toBe(false)
    expect(isClashNodeLinkText('http://1.2.3.4:8080')).toBe(false)
    expect(isClashNodeLinkText('ss:/broken')).toBe(false)
    expect(isClashNodeLinkText('')).toBe(false)
    expect(isClashNodeLinkText(null)).toBe(false)
  })

  it('reads the node name from the fragment or the vmess JSON', () => {
    expect(clashNodeLinkName(ssLink)).toBe('🇺🇸 美国-洛杉矶 04')
    expect(clashNodeLinkName(vmessLink)).toBe('JP 01')
    expect(clashNodeLinkName('trojan://pw@a.example.com:443#%E6%97%A5%E6%9C%AC%2001')).toBe('日本 01')
    expect(clashNodeLinkName('trojan://pw@a.example.com:443#100%')).toBe('100%')
    expect(clashNodeLinkName('vmess://not-base64#Fallback')).toBe('Fallback')
    expect(clashNodeLinkName('trojan://pw@a.example.com:443')).toBe('')
  })

  it('counts the links of pasted text and names the first one', () => {
    expect(summarizeClashNodeLinks(`${ssLink}\r\n\r\n${vmessLink}\nnot a link`)).toEqual({ count: 2, firstName: '🇺🇸 美国-洛杉矶 04' })
    expect(summarizeClashNodeLinks('proxies: []')).toEqual({ count: 0, firstName: '' })
  })
})
