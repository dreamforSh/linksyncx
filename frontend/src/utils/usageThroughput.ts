/**
 * 单条使用记录的输出速度（token/s）。
 *
 * 有首字时间（流式）时按首字之后的生成窗口计算：output / (总耗时 − 首字)，
 * 与常见的 decode 吞吐口径一致，不受排队/预填充耗时影响；
 * 无首字时间（非流式）或生成窗口过短（整段输出几乎同时到达，按窗口算会虚高）
 * 时退回按总耗时计算。没有输出或没有耗时的记录返回 null。
 */
export const MIN_GENERATION_WINDOW_MS = 100

export interface ThroughputSource {
  output_tokens?: number | null
  duration_ms?: number | null
  first_token_ms?: number | null
}

export function outputTokensPerSecond(row: ThroughputSource): number | null {
  const output = row.output_tokens ?? 0
  const duration = row.duration_ms ?? 0
  if (!(output > 0) || !(duration > 0)) return null
  let windowMs = duration
  const firstToken = row.first_token_ms
  if (firstToken != null && firstToken >= 0 && duration - firstToken >= MIN_GENERATION_WINDOW_MS) {
    windowMs = duration - firstToken
  }
  return output / (windowMs / 1000)
}

/** 100 以下保留 1 位小数，以上取整并按千分位分组。 */
export function formatTokensPerSecond(value: number): string {
  const digits = value < 100 ? 1 : 0
  return value.toLocaleString(undefined, { minimumFractionDigits: digits, maximumFractionDigits: digits })
}

/** 导出用数值：保留 2 位小数，无值时为空串。 */
export function exportTokensPerSecond(row: ThroughputSource): number | '' {
  const value = outputTokensPerSecond(row)
  return value == null ? '' : Math.round(value * 100) / 100
}
