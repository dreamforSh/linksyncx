import { afterEach, describe, expect, it, vi } from 'vitest'

import { formatCurrency, formatDateTime } from '../format'

describe('format Intl formatter cache', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('reuses one DateTimeFormat per locale and options', () => {
    const value = new Date(2026, 0, 2, 3, 4, 5)
    const first = formatDateTime(value, undefined, 'en-GB')
    const spy = vi.spyOn(Intl, 'DateTimeFormat')

    expect(formatDateTime(value, undefined, 'en-GB')).toBe(first)
    expect(formatDateTime(new Date(2026, 5, 6), undefined, 'en-GB')).toContain('06/06/2026')
    expect(spy).not.toHaveBeenCalled()

    // 不同选项仍各自得到正确结果
    expect(formatDateTime(value, { year: 'numeric' }, 'en-GB')).toBe('2026')
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('keeps currency precision rules while caching NumberFormat', () => {
    expect(formatCurrency(1.5)).toMatch(/1\.50/)
    expect(formatCurrency(0.001234)).toMatch(/0\.001234/)
    expect(formatCurrency(2.25)).toMatch(/2\.25/)
  })
})
