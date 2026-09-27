<template>
  <AppLayout>
    <div class="mx-auto max-w-[1600px] space-y-5">
      <div
        v-if="isPoolDisabled"
        role="alert"
        class="flex items-start gap-3 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
        data-testid="clash-disabled-banner"
      >
        <Icon name="exclamationTriangle" size="md" class="mt-0.5 flex-shrink-0" />
        <div class="min-w-0">
          <p class="text-sm font-semibold">{{ t('admin.clash.disabledBanner.title') }}</p>
          <p class="mt-0.5 text-sm text-amber-800/90 dark:text-amber-200/80">{{ t('admin.clash.disabledBanner.description') }}</p>
        </div>
      </div>

      <div class="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-5">
        <StatTile
          :label="t('admin.clash.stats.profiles')"
          icon="cloud"
          :value="String(profiles.length)"
          :loading="profilesLoading && !profilesLoaded"
        >
          {{ t('admin.clash.stats.profilesHint', { count: enabledProfileCount }) }}
        </StatTile>
        <StatTile
          :label="t('admin.clash.stats.nodes')"
          icon="server"
          :value="String(totals.active)"
          :loading="profilesLoading && !profilesLoaded"
        >
          {{ t('admin.clash.stats.nodesHint', { total: totals.total }) }}
        </StatTile>
        <StatTile
          :label="t('admin.clash.stats.healthy')"
          icon="checkCircle"
          :value="String(totals.healthy)"
          :loading="profilesLoading && !profilesLoaded"
        >
          <span :class="totals.unhealthy > 0 ? 'text-red-600 dark:text-red-400' : ''">
            {{ t('admin.clash.stats.healthyHint', { count: totals.unhealthy }) }}
          </span>
        </StatTile>
        <StatTile
          :label="t('admin.clash.stats.bound')"
          icon="link"
          :value="String(totals.boundAccounts)"
          :loading="profilesLoading && !profilesLoaded"
        >
          {{ settings ? t('admin.clash.stats.boundHint', { max: settings.max_accounts_per_exit }) : t('admin.clash.stats.boundHintUnknown') }}
        </StatTile>
        <StatTile
          :label="t('admin.clash.stats.trafficToday')"
          icon="chartBar"
          :value="formatTrafficBytes(totals.trafficToday)"
          :loading="profilesLoading && !profilesLoaded"
          data-testid="clash-stat-traffic"
        >
          <span :title="t('admin.clash.nodes.traffic.approxHint')">
            {{ t('admin.clash.stats.trafficTotalHint', { total: formatTrafficBytes(totals.trafficTotal) }) }}
          </span>
        </StatTile>
      </div>

      <ClashRuntimeCard
        :runtime="runtime"
        :loading="runtimeLoading"
        :error="runtimeError"
        :resyncing="resyncing"
        @resync="handleResync"
      />

      <ClashProfileTable
        :profiles="profiles"
        :loading="profilesLoading"
        :readonly="isPoolDisabled"
        :refreshing-ids="refreshingIds"
        :toggling-ids="togglingIds"
        @create="openCreate"
        @edit="openEdit"
        @refresh="(profile) => refreshProfile(profile, false)"
        @force-refresh="(profile) => askForceRefresh(profile, profile.last_refresh_error)"
        @toggle="handleToggle"
        @view-nodes="viewNodes"
        @delete="requestDelete"
      >
        <template #actions>
          <div class="flex flex-wrap items-center gap-2">
            <button
              type="button"
              class="btn btn-secondary px-2.5"
              :disabled="profilesLoading || runtimeLoading"
              :title="t('common.refresh')"
              :aria-label="t('common.refresh')"
              data-testid="clash-reload"
              @click="reloadAll"
            >
              <Icon name="refresh" size="sm" :class="profilesLoading || runtimeLoading ? 'animate-spin' : ''" />
            </button>
            <button type="button" class="btn btn-secondary" data-testid="clash-open-settings" @click="showSettings = true">
              <Icon name="cog" size="sm" />
              {{ t('admin.clash.actions.settings') }}
            </button>
            <button
              type="button"
              class="btn btn-primary"
              :disabled="isPoolDisabled"
              data-testid="clash-add-profile"
              @click="openCreate"
            >
              <Icon name="plus" size="sm" :stroke-width="2" />
              {{ t('admin.clash.actions.addProfile') }}
            </button>
          </div>
        </template>
      </ClashProfileTable>

      <ClashNodesPanel
        ref="nodesPanelRef"
        :profiles="profiles"
        :readonly="isPoolDisabled"
        :max-accounts-per-exit="settings?.max_accounts_per_exit ?? null"
        @changed="handleNodesChanged"
      />
    </div>

    <ClashProfileFormDialog
      :show="showForm"
      :profile="editingProfile"
      :proxies="manualProxies"
      @close="closeForm"
      @saved="handleSaved"
    />

    <ClashSettingsDialog :show="showSettings" @close="showSettings = false" @saved="settings = $event" />

    <!-- Delete: plain confirmation first; the server reports bound accounts with 409 -->
    <ConfirmDialog
      :show="deleteTarget !== null && deleteInUseAccounts === null"
      :title="t('admin.clash.deleteDialog.title')"
      :message="t('admin.clash.deleteDialog.message', { name: deleteTarget?.name ?? '' })"
      :confirm-text="deleting ? t('common.processing') : t('common.delete')"
      danger
      @confirm="deleteProfile(false)"
      @cancel="closeDelete"
    />

    <BaseDialog
      :show="deleteTarget !== null && deleteInUseAccounts !== null"
      :title="t('admin.clash.deleteDialog.inUseTitle')"
      width="narrow"
      @close="closeDelete"
    >
      <div class="space-y-3 text-sm" data-testid="clash-delete-in-use">
        <p class="text-gray-600 dark:text-gray-400">
          {{ t('admin.clash.deleteDialog.inUseMessage', { name: deleteTarget?.name ?? '' }) }}
        </p>
        <div v-if="deleteInUseAccounts && deleteInUseAccounts.length > 0" class="flex flex-wrap gap-1.5">
          <span
            v-for="name in deleteInUseAccounts"
            :key="name"
            class="rounded-md bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-700 dark:bg-dark-700 dark:text-gray-300"
          >{{ name }}</span>
        </div>
        <p class="rounded-lg bg-amber-50 p-3 text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">
          {{ t('admin.clash.deleteDialog.forceHint') }}
        </p>
      </div>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" :disabled="deleting" @click="closeDelete">{{ t('common.cancel') }}</button>
          <button type="button" class="btn btn-danger" :disabled="deleting" data-testid="clash-force-delete" @click="deleteProfile(true)">
            {{ deleting ? t('common.processing') : t('admin.clash.actions.forceDelete') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <ConfirmDialog
      :show="forceRefreshTarget !== null"
      :title="t('admin.clash.refresh.forceTitle')"
      :message="t('admin.clash.refresh.forceMessage', { name: forceRefreshTarget?.profile.name ?? '' })"
      :confirm-text="t('admin.clash.actions.forceRefresh')"
      danger
      @confirm="confirmForceRefresh"
      @cancel="forceRefreshTarget = null"
    >
      <p
        v-if="forceRefreshTarget?.reason"
        class="break-all rounded-lg bg-amber-50 p-3 text-xs text-amber-800 dark:bg-amber-500/10 dark:text-amber-200"
      >{{ forceRefreshTarget.reason }}</p>
    </ConfirmDialog>

    <ConfirmDialog
      :show="disableTarget !== null"
      :title="t('admin.clash.toggleDialog.title')"
      :message="t('admin.clash.toggleDialog.message', { name: disableTarget?.name ?? '', count: boundAccountsOf(disableTarget) })"
      :confirm-text="t('admin.clash.actions.disable')"
      danger
      @confirm="confirmDisableProfile"
      @cancel="disableTarget = null"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import StatTile from '@/components/common/StatTile.vue'
import Icon from '@/components/icons/Icon.vue'
import ClashRuntimeCard from '@/components/admin/clash/ClashRuntimeCard.vue'
import ClashProfileTable from '@/components/admin/clash/ClashProfileTable.vue'
import ClashNodesPanel from '@/components/admin/clash/ClashNodesPanel.vue'
import ClashProfileFormDialog from '@/components/admin/clash/ClashProfileFormDialog.vue'
import ClashSettingsDialog from '@/components/admin/clash/ClashSettingsDialog.vue'
import type { ClashPoolSettings, ClashProfile, ClashRuntimeStatus, Proxy } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'
import { clashErrorAccounts, clashErrorCode, clashErrorMessage, formatTrafficBytes } from '@/utils/clash'

const { t } = useI18n()
const appStore = useAppStore()

const runtime = ref<ClashRuntimeStatus | null>(null)
const runtimeLoading = ref(false)
const runtimeError = ref('')
const resyncing = ref(false)

const profiles = ref<ClashProfile[]>([])
const profilesLoading = ref(false)
const profilesLoaded = ref(false)
const settings = ref<ClashPoolSettings | null>(null)
const manualProxies = ref<Proxy[]>([])
let manualProxiesLoaded = false

const refreshingIds = ref(new Set<number>())
const togglingIds = ref(new Set<number>())

const showForm = ref(false)
const editingProfile = ref<ClashProfile | null>(null)
const showSettings = ref(false)

const deleteTarget = ref<ClashProfile | null>(null)
const deleteInUseAccounts = ref<string[] | null>(null)
const deleting = ref(false)
const forceRefreshTarget = ref<{ profile: ClashProfile; reason: string } | null>(null)
const disableTarget = ref<ClashProfile | null>(null)

const nodesPanelRef = ref<InstanceType<typeof ClashNodesPanel> | null>(null)

const isPoolDisabled = computed(() => runtime.value?.mode === 'disabled')
// Older backends only report bound nodes; with one account per exit they match.
const boundAccountsOf = (profile: ClashProfile | null) =>
  profile?.stats?.bound_accounts ?? profile?.stats?.bound ?? 0
const enabledProfileCount = computed(() => profiles.value.filter((profile) => profile.enabled).length)
const totals = computed(() =>
  profiles.value.reduce(
    (acc, profile) => ({
      total: acc.total + (profile.stats?.total ?? 0),
      active: acc.active + (profile.stats?.active ?? 0),
      healthy: acc.healthy + (profile.stats?.healthy ?? 0),
      unhealthy: acc.unhealthy + (profile.stats?.unhealthy ?? 0),
      boundAccounts: acc.boundAccounts + boundAccountsOf(profile),
      trafficToday: acc.trafficToday + (profile.measured_traffic?.today_upload_bytes ?? 0) +
        (profile.measured_traffic?.today_download_bytes ?? 0),
      trafficTotal: acc.trafficTotal + (profile.measured_traffic?.upload_bytes ?? 0) +
        (profile.measured_traffic?.download_bytes ?? 0)
    }),
    { total: 0, active: 0, healthy: 0, unhealthy: 0, boundAccounts: 0, trafficToday: 0, trafficTotal: 0 }
  )
)

const describeError = (error: unknown, fallbackKey: string) =>
  clashErrorMessage(error, t) ?? extractApiErrorMessage(error, t(fallbackKey))

const withId = (set: typeof refreshingIds, id: number, active: boolean) => {
  const next = new Set(set.value)
  if (active) next.add(id)
  else next.delete(id)
  set.value = next
}

async function loadRuntime() {
  runtimeLoading.value = true
  try {
    runtime.value = await adminAPI.clash.getRuntime()
    runtimeError.value = ''
  } catch (error) {
    runtimeError.value = describeError(error, 'admin.clash.runtime.loadFailed')
  } finally {
    runtimeLoading.value = false
  }
}

async function loadProfiles() {
  profilesLoading.value = true
  try {
    profiles.value = await adminAPI.clash.listProfiles()
    profilesLoaded.value = true
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.profiles.loadFailed'))
  } finally {
    profilesLoading.value = false
  }
}

async function loadSettings() {
  try {
    settings.value = await adminAPI.clash.getSettings()
  } catch {
    // Only used for the "per exit" hint; the settings dialog reports its own errors.
    settings.value = null
  }
}

async function ensureManualProxies() {
  if (manualProxiesLoaded) return
  try {
    manualProxies.value = await adminAPI.proxies.getAll()
    manualProxiesLoaded = true
  } catch {
    manualProxies.value = []
  }
}

function reloadAll() {
  void loadRuntime()
  void loadProfiles()
  void loadSettings()
  void nodesPanelRef.value?.reload()
}

async function handleResync() {
  if (resyncing.value) return
  resyncing.value = true
  try {
    runtime.value = await adminAPI.clash.resyncRuntime()
    runtimeError.value = ''
    appStore.showSuccess(t('admin.clash.runtime.resyncSuccess'))
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.runtime.resyncFailed'))
  } finally {
    resyncing.value = false
  }
}

function openCreate() {
  if (isPoolDisabled.value) return
  editingProfile.value = null
  showForm.value = true
  void ensureManualProxies()
}

function openEdit(profile: ClashProfile) {
  editingProfile.value = profile
  showForm.value = true
  void ensureManualProxies()
}

function closeForm() {
  showForm.value = false
  editingProfile.value = null
}

function handleSaved() {
  void loadProfiles()
  void nodesPanelRef.value?.reload()
}

async function refreshProfile(profile: ClashProfile, force: boolean) {
  if (refreshingIds.value.has(profile.id)) return
  withId(refreshingIds, profile.id, true)
  try {
    const result = await adminAPI.clash.refreshProfile(profile.id, { force })
    if (result.status === 'ok') {
      appStore.showSuccess(t('admin.clash.refresh.success', {
        name: profile.name,
        inserted: result.inserted,
        updated: result.updated,
        missing: result.missing
      }))
    } else if (result.status === 'skipped') {
      askForceRefresh(profile, result.error || '')
    } else {
      appStore.showError(t('admin.clash.refresh.failed', { name: profile.name, error: result.error || '-' }))
    }
    await loadProfiles()
    void nodesPanelRef.value?.reload()
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.refresh.requestFailed'))
  } finally {
    withId(refreshingIds, profile.id, false)
  }
}

function askForceRefresh(profile: ClashProfile, reason: string) {
  forceRefreshTarget.value = { profile, reason }
}

function confirmForceRefresh() {
  const target = forceRefreshTarget.value
  forceRefreshTarget.value = null
  if (target) void refreshProfile(target.profile, true)
}

async function setProfileEnabled(profile: ClashProfile, enabled: boolean) {
  withId(togglingIds, profile.id, true)
  try {
    await adminAPI.clash.updateProfile(profile.id, { enabled })
    appStore.showSuccess(enabled ? t('admin.clash.profiles.enabledToast') : t('admin.clash.profiles.disabledToast'))
    await loadProfiles()
    void nodesPanelRef.value?.reload()
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.profiles.toggleFailed'))
  } finally {
    withId(togglingIds, profile.id, false)
  }
}

function handleToggle(profile: ClashProfile, enabled: boolean) {
  // Disabling pauses every account bound to this subscription's exits: confirm first.
  if (!enabled && boundAccountsOf(profile) > 0) {
    disableTarget.value = profile
    return
  }
  void setProfileEnabled(profile, enabled)
}

function confirmDisableProfile() {
  const profile = disableTarget.value
  disableTarget.value = null
  if (profile) void setProfileEnabled(profile, false)
}

function viewNodes(profile: ClashProfile) {
  nodesPanelRef.value?.focusProfile(profile.id)
}

function handleNodesChanged() {
  void loadProfiles()
}

function requestDelete(profile: ClashProfile) {
  deleteTarget.value = profile
  deleteInUseAccounts.value = null
}

function closeDelete() {
  if (deleting.value) return
  deleteTarget.value = null
  deleteInUseAccounts.value = null
}

async function deleteProfile(force: boolean) {
  const profile = deleteTarget.value
  if (!profile || deleting.value) return
  deleting.value = true
  try {
    await adminAPI.clash.deleteProfile(profile.id, { force })
    appStore.showSuccess(t('admin.clash.deleteDialog.success', { name: profile.name }))
    deleteTarget.value = null
    deleteInUseAccounts.value = null
    await loadProfiles()
    void nodesPanelRef.value?.reload()
  } catch (error) {
    if (!force && clashErrorCode(error) === 'CLASH_PROFILE_IN_USE') {
      deleteInUseAccounts.value = clashErrorAccounts(error)
        .split(',')
        .map((name) => name.trim())
        .filter(Boolean)
      return
    }
    appStore.showError(describeError(error, 'admin.clash.deleteDialog.failed'))
  } finally {
    deleting.value = false
  }
}

onMounted(() => {
  void loadRuntime()
  void loadProfiles()
  void loadSettings()
})
</script>
