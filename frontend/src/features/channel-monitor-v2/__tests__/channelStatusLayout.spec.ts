import { describe, expect, it } from 'vitest'
import { buildChannelStatusAxis, buildChannelStatusLayout, labelChannelStatusAxis } from '../channelStatusLayout'
import { cachedDateTimeFormat, monitorHasTraffic } from '../monitorFormat'

const HOUR = 3600 * 1000
const END = Date.parse('2026-10-19T12:00:00Z')

describe('buildChannelStatusAxis', () => {
  it('spans [requested_start, requested_end) in bucket steps', () => {
    const axis = buildChannelStatusAxis({
      requested_start: new Date(END - 24 * HOUR).toISOString(),
      requested_end: new Date(END).toISOString(),
      data_through: new Date(END - 2 * HOUR).toISOString(),
      bucket_seconds: 3600,
    })
    expect(axis).toHaveLength(24)
    expect(axis[0]).toBe(END - 24 * HOUR)
    expect(axis[23]).toBe(END - HOUR)
  })

  it('falls back to data_through without requested_end and rejects broken coverage', () => {
    expect(buildChannelStatusAxis({
      requested_start: new Date(END - 90 * 60 * 1000).toISOString(),
      data_through: new Date(END - 3 * 60 * 1000).toISOString(),
      bucket_seconds: 300,
    })).toHaveLength(18)
    expect(buildChannelStatusAxis(null)).toEqual([])
    expect(buildChannelStatusAxis({ requested_start: 'nope', data_through: 'nope', bucket_seconds: 300 })).toEqual([])
    expect(buildChannelStatusAxis({
      requested_start: new Date(END).toISOString(),
      requested_end: new Date(END - HOUR).toISOString(),
      data_through: new Date(END - HOUR).toISOString(),
      bucket_seconds: 300,
    })).toEqual([])
  })

  it('labels each column once with a shared formatter', () => {
    const slots = labelChannelStatusAxis([END - HOUR, END], 'zh-CN', time => `${time} empty`)
    expect(slots).toHaveLength(2)
    expect(slots[0].start).toBe(END - HOUR)
    expect(slots[0].emptyLabel).toBe(`${slots[0].time} empty`)
    const options: Intl.DateTimeFormatOptions = { hour: '2-digit' }
    expect(cachedDateTimeFormat('zh-CN', 'probe', options)).toBe(cachedDateTimeFormat('zh-CN', 'probe', options))
    expect(cachedDateTimeFormat('en-US', 'probe', options)).not.toBe(cachedDateTimeFormat('zh-CN', 'probe', options))
  })
})

describe('monitorHasTraffic', () => {
  const none = { request_count: 0, rpm: 0, tpm: 0, error_rate: 0, ttft: { p50_ms: null }, duration: { p50_ms: null } }

  it('treats an empty window as no traffic even though error_rate is 0', () => {
    expect(monitorHasTraffic(none, { overall: 'unknown', error_rate: 'unknown' })).toBe(false)
  })

  it('detects traffic in redacted payloads from the signals that survive redaction', () => {
    expect(monitorHasTraffic({ ...none, duration: { p50_ms: 1200 } })).toBe(true)
    expect(monitorHasTraffic({ ...none, error_rate: 0.5 })).toBe(true)
    expect(monitorHasTraffic(none, { overall: 'unknown', error_rate: 'healthy' })).toBe(true)
    expect(monitorHasTraffic({ ...none, request_count: 3 })).toBe(true)
  })
})

describe('buildChannelStatusLayout', () => {
  it('keeps multi-group platforms as clusters and packs single-group platforms together', () => {
    const blocks = buildChannelStatusLayout([
      { platform: 'a', rows: [1, 2] },
      { platform: 'b', rows: [3] },
      { platform: 'c', rows: [4] },
      { platform: 'd', rows: [5, 6] },
    ])
    expect(blocks.map(block => block.kind)).toEqual(['cluster', 'compact', 'cluster'])
  })
})
