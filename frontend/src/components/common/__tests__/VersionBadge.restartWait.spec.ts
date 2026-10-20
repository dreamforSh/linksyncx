import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

import VersionBadge from '../VersionBadge.vue'
import { RESTART_TOTAL_LIMIT_MS } from '@/utils/serviceRestart'

const { performUpdate, restartService, waitForServiceRestart } = vi.hoisted(() => ({
  performUpdate: vi.fn(),
  restartService: vi.fn(),
  waitForServiceRestart: vi.fn()
}))

// t 把参数拼进返回值，便于断言提示文案里的分钟数。
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}${JSON.stringify(params)}` : key
    })
  }
})

vi.mock('@/stores', () => ({
  useAuthStore: () => ({ isAdmin: true }),
  useAppStore: () => ({
    versionLoading: false,
    currentVersion: '0.2.9',
    latestVersion: '0.3.0',
    hasUpdate: true,
    releaseInfo: null,
    buildType: 'release',
    fetchVersion: vi.fn(),
    clearVersionCache: vi.fn()
  })
}))

vi.mock('@/api/admin/system', () => ({
  performUpdate,
  restartService,
  getRollbackVersions: vi.fn(),
  rollback: vi.fn()
}))

vi.mock('@/utils/serviceRestart', async () => {
  const actual =
    await vi.importActual<typeof import('@/utils/serviceRestart')>('@/utils/serviceRestart')
  return { ...actual, waitForServiceRestart }
})

const buttonWithText = (wrapper: VueWrapper, text: string) => {
  const button = wrapper.findAll('button').find((candidate) => candidate.text().includes(text))
  if (!button) throw new Error(`button "${text}" not rendered`)
  return button
}

// 走一遍“更新完成 → 立即重启”，停在等待新进程就绪的阶段。
const startRestart = async () => {
  const wrapper = mount(VersionBadge, { global: { stubs: { Icon: true } } })
  await wrapper.find('button').trigger('click')
  await buttonWithText(wrapper, 'version.updateNow').trigger('click')
  await flushPromises()
  await buttonWithText(wrapper, 'version.restartNow').trigger('click')
  await flushPromises()
  return wrapper
}

describe('VersionBadge restart wait', () => {
  let releaseWait: (result: string) => void = () => {}

  beforeEach(() => {
    // useClipboard 在 setup 里取真实的 app store。
    setActivePinia(createPinia())
    vi.clearAllMocks()
    performUpdate.mockResolvedValue({ message: 'ok', need_restart: true })
    waitForServiceRestart.mockImplementation(
      () =>
        new Promise((resolve) => {
          releaseWait = resolve
        })
    )
  })

  afterEach(() => {
    // 'cancelled' 不会触发 window.location.reload。
    releaseWait('cancelled')
  })

  it('按重启响应下发的宽限放宽等待上限，并在提示里给出对应的分钟数', async () => {
    restartService.mockResolvedValue({
      message: 'Service restart initiated',
      operation_id: 'sysop-restart-1',
      shutdown_timeout_seconds: 300
    })

    const wrapper = await startRestart()

    expect(restartService).toHaveBeenCalledTimes(1)
    // 宽限 300s + 强制 5s + 清理 30s + 拉起启动 60s。
    expect(waitForServiceRestart).toHaveBeenCalledWith(
      expect.objectContaining({ totalLimitMs: 395_000 })
    )
    expect(wrapper.text()).toContain('version.restartingHint{"minutes":7}')
    wrapper.unmount()
  })

  it('旧后端不下发宽限或重启请求本身断开时，退回默认上限', async () => {
    restartService.mockResolvedValueOnce({ message: 'Service restart initiated' })
    let wrapper = await startRestart()
    expect(waitForServiceRestart).toHaveBeenLastCalledWith(
      expect.objectContaining({ totalLimitMs: RESTART_TOTAL_LIMIT_MS })
    )
    expect(wrapper.text()).toContain('version.restartingHint{"minutes":2}')
    wrapper.unmount()
    releaseWait('cancelled')

    restartService.mockRejectedValueOnce(new Error('Network Error'))
    wrapper = await startRestart()
    expect(waitForServiceRestart).toHaveBeenLastCalledWith(
      expect.objectContaining({ totalLimitMs: RESTART_TOTAL_LIMIT_MS })
    )
    expect(wrapper.text()).toContain('version.restartingHint{"minutes":2}')
    wrapper.unmount()
  })
})
