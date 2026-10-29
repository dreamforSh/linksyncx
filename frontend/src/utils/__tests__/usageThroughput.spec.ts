import { describe, expect, it } from 'vitest'

import { exportTokensPerSecond, formatTokensPerSecond, outputTokensPerSecond } from '../usageThroughput'

describe('usageThroughput', () => {
  it('measures streaming speed over the window after the first token', () => {
    // 500 tokens generated in the 5s between first token (1s) and completion (6s).
    expect(outputTokensPerSecond({ output_tokens: 500, first_token_ms: 1_000, duration_ms: 6_000 })).toBe(100)
  })

  it('falls back to total duration without a first-token time', () => {
    expect(outputTokensPerSecond({ output_tokens: 300, first_token_ms: null, duration_ms: 6_000 })).toBe(50)
    expect(outputTokensPerSecond({ output_tokens: 300, duration_ms: 6_000 })).toBe(50)
  })

  it('falls back to total duration when the output arrived in one burst', () => {
    // 99ms generation window is below the minimum, so 2000 tokens over the full 2s.
    expect(outputTokensPerSecond({ output_tokens: 2_000, first_token_ms: 1_901, duration_ms: 2_000 })).toBe(1_000)
    expect(outputTokensPerSecond({ output_tokens: 10, first_token_ms: 2_500, duration_ms: 2_000 })).toBe(5)
    // Exactly the minimum window still counts as a real generation window.
    expect(outputTokensPerSecond({ output_tokens: 10, first_token_ms: 900, duration_ms: 1_000 })).toBe(100)
  })

  it('returns null without output tokens or duration', () => {
    expect(outputTokensPerSecond({ output_tokens: 0, first_token_ms: 100, duration_ms: 1_000 })).toBeNull()
    expect(outputTokensPerSecond({ output_tokens: 10, first_token_ms: null, duration_ms: 0 })).toBeNull()
    expect(outputTokensPerSecond({ output_tokens: 10, duration_ms: null })).toBeNull()
    expect(outputTokensPerSecond({})).toBeNull()
  })

  it('formats with one decimal below 100 and none above', () => {
    expect(formatTokensPerSecond(42.345)).toBe((42.3).toLocaleString(undefined, { minimumFractionDigits: 1 }))
    expect(formatTokensPerSecond(1_234.6)).toBe((1_235).toLocaleString())
  })

  it('exports a rounded number or an empty cell', () => {
    expect(exportTokensPerSecond({ output_tokens: 100, duration_ms: 3_000 })).toBe(33.33)
    expect(exportTokensPerSecond({ output_tokens: 0, duration_ms: 3_000 })).toBe('')
  })
})
