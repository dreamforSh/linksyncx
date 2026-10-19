import type { MonitorCoverage } from '@/api/channelMonitorV2'
import { cachedDateTimeFormat } from './monitorFormat'

export type ChannelStatusPlatformSection<T> = {
  platform: string
  rows: T[]
}

export type ChannelStatusLayoutBlock<T> =
  | { kind: 'cluster'; platform: string; rows: T[] }
  | { kind: 'compact'; items: Array<{ platform: string; row: T }> }

// Platforms with 2+ groups keep a labeled cluster. Consecutive single-group
// platforms share one grid row so they do not each occupy a full-width band.
export function buildChannelStatusLayout<T>(
  sections: ChannelStatusPlatformSection<T>[],
): ChannelStatusLayoutBlock<T>[] {
  const blocks: ChannelStatusLayoutBlock<T>[] = []
  let compact: Array<{ platform: string; row: T }> = []

  const flushCompact = () => {
    if (compact.length === 0) return
    blocks.push({ kind: 'compact', items: compact })
    compact = []
  }

  for (const section of sections) {
    if (section.rows.length >= 2) {
      flushCompact()
      blocks.push({ kind: 'cluster', platform: section.platform, rows: section.rows })
      continue
    }
    if (section.rows.length === 1) {
      compact.push({ platform: section.platform, row: section.rows[0] })
    }
  }
  flushCompact()
  return blocks
}

export function channelStatusLayoutBlockKey<T>(block: ChannelStatusLayoutBlock<T>, index: number): string {
  if (block.kind === 'cluster') return `cluster:${block.platform}`
  return `compact:${index}:${block.items.map(item => item.platform).join('|')}`
}

export type ChannelStatusAxisCoverage = Pick<
  MonitorCoverage,
  'requested_start' | 'requested_end' | 'data_through' | 'bucket_seconds'
>

/** One timeline column shared by every card: epoch-ms start plus its pre-formatted labels. */
export interface ChannelStatusAxisSlot {
  start: number
  /** Formatted bucket start, reused by the tooltip. */
  time: string
  /** Accessible name / tooltip for a slot without data. */
  emptyLabel: string
}

/** Guards against a malformed coverage turning into a runaway loop (30d @ 5m would be 8640). */
const MAX_AXIS_SLOTS = 2000

/**
 * Bucket starts (epoch ms) spanning the selected range [requested_start, requested_end).
 * The backend only emits buckets that had traffic, so cards must place each bucket on
 * this axis by bucket_start instead of packing the sparse list (same axis as
 * RelayPulseMatrix). Falls back to data_through for payloads without requested_end.
 */
export function buildChannelStatusAxis(coverage: ChannelStatusAxisCoverage | null | undefined): number[] {
  if (!coverage) return []
  const step = Math.max(60, Number(coverage.bucket_seconds) || 0) * 1000
  const requestedStart = Date.parse(coverage.requested_start)
  const requestedEnd = coverage.requested_end ? Date.parse(coverage.requested_end) : NaN
  const end = Number.isFinite(requestedEnd) && requestedEnd > requestedStart
    ? requestedEnd
    : Date.parse(coverage.data_through)
  if (!Number.isFinite(requestedStart) || !Number.isFinite(end) || requestedStart >= end) return []
  const starts: number[] = []
  for (let cursor = Math.floor(requestedStart / step) * step; cursor < end && starts.length < MAX_AXIS_SLOTS; cursor += step) {
    starts.push(cursor)
  }
  return starts
}

const BUCKET_TIME_OPTIONS: Intl.DateTimeFormatOptions = { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }

/**
 * Formats each axis column once per axis (not per card × bucket); every card shares the
 * resulting labels.
 */
export function labelChannelStatusAxis(
  starts: readonly number[],
  locale: string | undefined,
  emptyLabel: (time: string) => string,
): ChannelStatusAxisSlot[] {
  const format = cachedDateTimeFormat(locale, 'channel-status-bucket', BUCKET_TIME_OPTIONS)
  return starts.map((start) => {
    const time = format.format(start)
    return { start, time, emptyLabel: emptyLabel(time) }
  })
}
