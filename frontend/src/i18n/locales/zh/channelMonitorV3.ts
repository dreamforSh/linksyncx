/** Compact V3 presentation for the V2 passive channel monitor. */
export default {
  channelMonitorV3: {
    title: '渠道状态',
    description: 'V2 被动用量 · 缓存率与成功率',
    updatedTo: '更新至 {time}',
    partialCoverage: '部分历史覆盖',
    loadFailed: '渠道状态加载失败',
    cacheRate: '缓存率',
    successRate: '成功率',
    ttft: '首 Token',
    unknown: '样本不足',
    allPlatforms: '全部',
    userRate: '用户倍率',
    unknownGroup: '未知分组',
    timelineTooltip: '{time} · 可用率 {availability} · 缓存率 {cache} · 首 Token {ttft}',
    emptyTitle: '暂无渠道数据',
    emptyDescription: '当前时间范围内还没有可展示的被动监控数据',
    summary: '成功率 {success} · 缓存率 {cache}',
    ranges: { '90m': '90m', '24h': '24h', '7d': '7d', '30d': '30d' },
  },
}
