/** Compact V3 presentation for the V2 passive channel monitor. */
export default {
  channelMonitorV3: {
    title: 'Channel status',
    description: 'V2 passive usage · cache and success rates',
    updatedTo: 'Updated to {time}',
    partialCoverage: 'Partial history coverage',
    loadFailed: 'Failed to load channel status',
    cacheRate: 'Cache rate',
    successRate: 'Success rate',
    ttft: 'First token',
    unknown: 'Insufficient samples',
    allPlatforms: 'All',
    userRate: 'User rate',
    unknownGroup: 'Unknown group',
    timelineTooltip: '{time} · Availability {availability} · Cache {cache} · First token {ttft}',
    emptyTitle: 'No channel data',
    emptyDescription: 'There is no passive-monitor data for this time range yet',
    summary: 'Success rate {success} · Cache {cache}',
    ranges: { '90m': '90m', '24h': '24h', '7d': '7d', '30d': '30d' },
  },
}
