import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ChannelMonitor } from '@/api/admin/channelMonitor'
import ChannelMonitorView from '@/views/admin/ChannelMonitorView.vue'

// V1 列表：状态汇总、可用率配色、最近检测（含停滞告警）与停用标记。

const { listMonitors } = vi.hoisted(() => ({ listMonitors: vi.fn() }))

vi.mock('@/utils/featureFlags', () => ({
  isChannelMonitorV1Mode: () => true,
  isChannelMonitorV2Mode: () => false,
  getChannelMonitorMode: () => 'v1' as const,
}))

vi.mock('@/features/channel-monitor-v2/MonitorSettingsPanel.vue', () => ({
  default: { name: 'MonitorSettingsPanel', template: '<div />' },
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { channelMonitor: { list: listMonitors, duplicate: vi.fn(), update: vi.fn(), runNow: vi.fn(), del: vi.fn() } },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }),
  }
})

const AppLayoutStub = defineComponent({ template: '<main><slot /></main>' })
const TablePageLayoutStub = defineComponent({
  template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>',
})
const DataTableStub = defineComponent({
  props: { data: { type: Array, default: () => [] }, columns: { type: Array, default: () => [] }, loading: Boolean },
  template: `<div>
    <div v-for="row in data" :key="row.id" :data-row="row.id">
      <div class="name-cell"><slot name="cell-name" :row="row" :value="row.name" /></div>
      <div class="availability-cell"><slot name="cell-availability_7d" :row="row" /></div>
      <div class="checked-cell"><slot name="cell-last_checked" :row="row" /></div>
    </div>
  </div>`,
})

function makeMonitor(overrides: Partial<ChannelMonitor> = {}): ChannelMonitor {
  return {
    id: 1, name: 'm', provider: 'openai', api_mode: 'chat_completions', endpoint: 'https://relay.example.com/v1',
    api_key_masked: 'sk-***', primary_model: 'gpt', extra_models: [], group_name: 'Codex', enabled: true,
    interval_seconds: 60, jitter_seconds: 0, last_checked_at: null, created_by: 1,
    created_at: '2026-07-16T00:00:00Z', updated_at: '2026-07-16T00:00:00Z', primary_status: 'operational',
    primary_latency_ms: 500, availability_7d: 99.5, extra_models_status: [], template_id: null, extra_headers: {},
    body_override_mode: 'off', body_override: null, check_mode: 'probe', account_id: null,
    ...overrides,
  }
}

function mountView() {
  return mount(ChannelMonitorView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub, TablePageLayout: TablePageLayoutStub, DataTable: DataTableStub,
        MonitorFiltersBar: true, Pagination: true, ConfirmDialog: true, EmptyState: true, HelpTooltip: true,
        Toggle: true, MonitorFormDialog: true, MonitorTemplateManagerDialog: true, MonitorRunResultDialog: true,
        MonitorPrimaryModelCell: true,
      },
    },
  })
}

describe('ChannelMonitorView V1 list status', () => {
  beforeEach(() => {
    localStorage.clear()
    listMonitors.mockReset()
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-10-18T12:00:00Z'))
  })
  afterEach(() => vi.useRealTimers())

  it('summarizes statuses, colors availability and flags stalled checks', async () => {
    listMonitors.mockResolvedValue({
      items: [
        makeMonitor({ id: 1, last_checked_at: '2026-10-18T11:59:00Z' }),
        makeMonitor({ id: 2, primary_status: 'failed', availability_7d: 71.3, last_checked_at: '2026-10-18T10:00:00Z' }),
        makeMonitor({ id: 3, enabled: false, primary_status: '', last_checked_at: null }),
      ],
      total: 3, page: 1, page_size: 20, pages: 1,
    })
    const wrapper = mountView()
    await flushPromises()

    const summary = wrapper.get('[data-testid="monitor-status-summary"]').text()
    expect(summary).toContain('admin.channelMonitor.list.summary.operational 1')
    expect(summary).toContain('admin.channelMonitor.list.summary.failed 1')
    expect(summary).toContain('admin.channelMonitor.list.summary.disabled 1')
    expect(summary).not.toContain('pageScope')

    const row1 = wrapper.get('[data-row="1"]')
    expect(row1.get('.availability-cell').html()).toContain('text-emerald-600')
    expect(row1.get('.checked-cell').text()).not.toContain('exclamation')
    expect(row1.get('.name-cell').text()).toContain('Codex · relay.example.com')

    const row2 = wrapper.get('[data-row="2"]')
    expect(row2.get('.availability-cell').html()).toContain('text-red-600')
    // 两小时没有检测，远超 3 × 60s：显示停滞告警
    expect(row2.get('.checked-cell').html()).toContain('text-amber-600')

    const row3 = wrapper.get('[data-row="3"]')
    expect(row3.get('.name-cell').text()).toContain('admin.channelMonitor.list.disabled')
    expect(row3.get('.checked-cell').text()).toContain('admin.channelMonitor.list.neverChecked')
    expect(row3.get('.availability-cell').text()).toBe('-')
    wrapper.unmount()
  })
})
