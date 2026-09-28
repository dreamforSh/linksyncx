<template>
  <section ref="rootRef" class="card scroll-mt-20 overflow-hidden" aria-labelledby="clash-nodes-title" data-testid="clash-nodes-panel">
    <div class="card-section-header">
      <div>
        <h2 id="clash-nodes-title" class="card-section-title">{{ t('admin.clash.nodes.title') }}</h2>
        <p class="card-section-subtitle">{{ t('admin.clash.nodes.subtitle', { count: pagination.total }) }}</p>
      </div>
      <div class="flex items-center gap-2">
        <SegmentedControl
          :model-value="viewMode"
          size="sm"
          :options="viewOptions"
          :aria-label="t('admin.clash.nodes.view.label')"
          test-id="clash-nodes-view"
          @update:model-value="setViewMode"
        />
        <AutoRefreshButton
          :enabled="autoRefresh.enabled.value"
          :interval-seconds="autoRefresh.intervalSeconds.value"
          :countdown="autoRefresh.countdown.value"
          :intervals="autoRefresh.intervals"
          @update:enabled="autoRefresh.setEnabled"
          @update:interval="autoRefresh.setInterval"
        />
        <button
          type="button"
          class="btn btn-secondary btn-sm px-2.5"
          :disabled="loading"
          :title="t('common.refresh')"
          :aria-label="t('common.refresh')"
          @click="load()"
        >
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
        </button>
      </div>
    </div>

    <div class="toolbar-compact flex flex-wrap items-center gap-2 px-5 pb-3">
      <div class="relative w-full sm:w-64">
        <Icon
          name="search"
          size="sm"
          class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-dark-400"
        />
        <input
          v-model="filters.search"
          type="text"
          class="input pl-9 pr-8"
          :placeholder="t('admin.clash.nodes.searchPlaceholder')"
          :aria-label="t('admin.clash.nodes.searchPlaceholder')"
          data-testid="clash-nodes-search"
          @input="onSearchInput"
        />
        <button
          v-if="filters.search"
          type="button"
          class="absolute right-2 top-1/2 flex h-5 w-5 -translate-y-1/2 items-center justify-center rounded text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-gray-200"
          :aria-label="t('common.clear')"
          @click="clearSearch"
        >
          <Icon name="x" size="xs" :stroke-width="2" />
        </button>
      </div>
      <div class="w-full sm:w-48">
        <Select
          v-model="filters.profileId"
          :options="profileOptions"
          :aria-label="t('admin.clash.nodes.filters.profile')"
          data-testid="clash-nodes-profile-filter"
          @change="applyFilters"
        />
      </div>
      <div class="w-full sm:w-32">
        <Select
          v-model="filters.status"
          :options="statusOptions"
          :aria-label="t('admin.clash.nodes.filters.status')"
          @change="applyFilters"
        />
      </div>
      <div class="w-full sm:w-36">
        <Select
          v-model="filters.visibility"
          :options="visibilityOptions"
          :aria-label="t('admin.clash.nodes.filters.visibility')"
          data-testid="clash-nodes-visibility-filter"
          @change="applyFilters"
        />
      </div>
      <div class="w-full sm:w-32">
        <Select
          v-model="filters.health"
          :options="healthOptions"
          :aria-label="t('admin.clash.nodes.filters.health')"
          @change="applyFilters"
        />
      </div>
      <SegmentedControl
        v-model="filters.bound"
        :options="boundOptions"
        :aria-label="t('admin.clash.nodes.filters.bound')"
        test-id="clash-nodes-bound-filter"
        @change="applyFilters"
      />
      <div class="w-full sm:w-40">
        <Select
          v-model="filters.sort"
          :options="sortOptions"
          :aria-label="t('admin.clash.nodes.sort.label')"
          data-testid="clash-nodes-sort"
          @change="applyFilters"
        />
      </div>

      <div v-if="selectedIds.length === 0" ref="batchMenuRef" class="relative sm:ml-auto">
        <button
          type="button"
          class="btn btn-secondary btn-sm"
          :disabled="readonly || batch.running.value"
          :aria-expanded="batchMenuOpen"
          aria-haspopup="menu"
          data-testid="clash-nodes-batch-menu"
          @click="batchMenuOpen = !batchMenuOpen"
        >
          <Icon name="play" size="sm" />
          {{ t('admin.clash.actions.batchTest') }}
          <Icon name="chevronDown" size="xs" />
        </button>
        <div
          v-if="batchMenuOpen"
          role="menu"
          class="absolute right-0 z-20 mt-1 w-64 overflow-hidden rounded-lg border border-gray-200 bg-white py-1 shadow-lg dark:border-dark-600 dark:bg-dark-800"
        >
          <p class="px-3 pb-1 pt-1.5 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.clash.nodes.batch.scopeFiltered') }}</p>
          <button
            v-for="item in batchMenuItems"
            :key="item.kind"
            type="button"
            role="menuitem"
            class="flex w-full flex-col items-start px-3 py-2 text-left text-sm hover:bg-gray-50 dark:hover:bg-dark-700"
            :data-testid="`clash-nodes-batch-${item.kind}`"
            @click="startBatch(item.kind, 'filtered')"
          >
            <span class="font-medium text-gray-900 dark:text-gray-100">{{ item.label }}</span>
            <span class="text-xs text-gray-500 dark:text-dark-400">{{ item.hint }}</span>
          </button>
        </div>
      </div>
    </div>

    <div
      v-if="loadError && nodes.length === 0"
      role="alert"
      class="mx-5 mb-3 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-500/10 dark:text-red-300"
    >
      {{ loadError }}
    </div>

    <div
      v-if="selectedIds.length > 0"
      class="flex flex-wrap items-center gap-2 border-y border-primary-200/70 bg-primary-50/80 px-5 py-2 dark:border-primary-500/20 dark:bg-primary-500/10"
      role="region"
      :aria-label="t('admin.clash.nodes.selectedCount', { count: selectedIds.length })"
      data-testid="clash-nodes-bulk-bar"
    >
      <span class="mr-1 text-sm font-medium text-primary-900 dark:text-primary-100">
        {{ t('admin.clash.nodes.selectedCount', { count: selectedIds.length }) }}
      </span>
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="readonly || batch.running.value"
        data-testid="clash-nodes-bulk-latency"
        @click="startBatch('latency', 'selected')"
      >
        <Icon name="bolt" size="sm" :class="runningKind === 'latency' ? 'animate-pulse' : ''" />
        {{ runningKind === 'latency' ? t('admin.clash.nodes.testingLatency') : t('admin.clash.actions.testLatency') }}
      </button>
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="readonly || batch.running.value"
        data-testid="clash-nodes-bulk-probe"
        @click="startBatch('exit', 'selected')"
      >
        <Icon name="globe" size="sm" :class="runningKind === 'exit' ? 'animate-pulse' : ''" />
        {{ runningKind === 'exit' ? t('admin.clash.nodes.probingExit') : t('admin.clash.actions.probeExit') }}
      </button>
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="readonly || batch.running.value"
        data-testid="clash-nodes-bulk-full"
        @click="startBatch('full', 'selected')"
      >
        <Icon name="shield" size="sm" :class="runningKind === 'full' ? 'animate-pulse' : ''" />
        {{ t('admin.clash.actions.fullCheck') }}
      </button>
      <span class="mx-0.5 hidden h-5 w-px bg-primary-200 dark:bg-primary-500/30 sm:inline-block" aria-hidden="true"></span>
      <button
        v-for="item in bulkActionItems"
        :key="item.action"
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="nodeActionRunning || batch.running.value"
        :data-testid="`clash-nodes-bulk-${item.action}`"
        @click="requestNodeAction(item.action, selectedIds)"
      >
        <Icon :name="item.icon" size="sm" />
        {{ item.label }}
      </button>
      <button
        type="button"
        class="ml-auto rounded-md px-2 py-1 text-sm font-medium text-primary-800 transition-colors hover:bg-primary-100 dark:text-primary-200 dark:hover:bg-primary-500/20"
        @click="selectedIds = []"
      >
        {{ t('admin.clash.actions.clearSelection') }}
      </button>
    </div>

    <div
      v-if="batch.progress.value"
      class="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-gray-100 px-5 py-2.5 text-sm dark:border-dark-700"
      role="status"
      aria-live="polite"
      data-testid="clash-nodes-batch-progress"
    >
      <span class="font-medium text-gray-900 dark:text-gray-100">
        {{ batchKindLabel(batch.progress.value.kind) }}
        <span class="tabular-nums text-gray-500 dark:text-dark-400">{{ batch.progress.value.done }}/{{ batch.progress.value.total }}</span>
      </span>
      <div class="h-1.5 min-w-[8rem] flex-1 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
        <div
          class="h-full rounded-full bg-primary-500 transition-[width] duration-300"
          :style="{ width: `${batchPercent}%` }"
        ></div>
      </div>
      <span class="flex items-center gap-3 text-xs tabular-nums">
        <span class="text-emerald-700 dark:text-emerald-300">{{ t('admin.clash.nodes.batch.success', { count: batch.progress.value.success }) }}</span>
        <span :class="batch.progress.value.failed > 0 ? 'text-red-600 dark:text-red-400' : 'text-gray-500 dark:text-dark-400'">
          {{ t('admin.clash.nodes.batch.failed', { count: batch.progress.value.failed }) }}
        </span>
        <span v-if="batch.progress.value.changed > 0" class="text-amber-700 dark:text-amber-300">
          {{ t('admin.clash.nodes.batch.changed', { count: batch.progress.value.changed }) }}
        </span>
        <span v-if="batch.progress.value.skipped > 0" class="text-gray-500 dark:text-dark-400">
          {{ t('admin.clash.nodes.batch.skipped', { count: batch.progress.value.skipped }) }}
        </span>
      </span>
      <button
        v-if="batch.running.value"
        type="button"
        class="btn btn-secondary btn-sm"
        data-testid="clash-nodes-batch-stop"
        @click="batch.stop()"
      >
        {{ t('admin.clash.actions.stop') }}
      </button>
      <button
        v-else
        type="button"
        class="icon-btn"
        :aria-label="t('common.close')"
        :title="t('common.close')"
        @click="batch.dismiss()"
      >
        <Icon name="x" size="sm" />
      </button>
    </div>

    <template v-if="viewMode === 'card'">
      <div
        v-if="nodes.length > 0"
        class="flex items-center gap-2 px-5 pb-1 pt-2 text-sm text-gray-600 dark:text-dark-300"
      >
        <label class="inline-flex cursor-pointer items-center gap-2">
          <input
            ref="selectPageRef"
            type="checkbox"
            class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500 dark:bg-dark-800"
            :checked="pageFullySelected"
            data-testid="clash-nodes-select-page"
            @change="togglePageSelection"
          />
          {{ t('admin.clash.nodes.selectPage') }}
        </label>
      </div>
      <div class="px-5 pb-5 pt-2" data-testid="clash-nodes-cards">
        <div v-if="loading && nodes.length === 0" class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
          <div v-for="index in 6" :key="index" class="skeleton h-72 rounded-xl"></div>
        </div>
        <EmptyState
          v-else-if="nodes.length === 0"
          :title="t('admin.clash.nodes.empty')"
          :description="t('admin.clash.nodes.emptyHint')"
        />
        <div
          v-else
          class="grid gap-4 transition-opacity sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4"
          :class="loading ? 'opacity-60' : ''"
        >
          <ClashNodeCard
            v-for="node in nodes"
            :key="node.id"
            :node="node"
            :selected="selectedSet.has(node.id)"
            :busy="busyIds.has(node.id)"
            :readonly="readonly"
            :max-per-exit="maxAccountsPerExit"
            @toggle-select="toggleSelected(node.id)"
            @test-latency="testOne(node)"
            @probe-exit="probeOne(node)"
            @enable="setNodeEnabled(node, true)"
            @disable="pendingDisable = node"
            @hide="requestNodeAction('hide', [node.id])"
            @unhide="requestNodeAction('unhide', [node.id])"
            @accept-exit="pendingAccept = node"
            @bindings="openBindings(node)"
          />
        </div>
      </div>
    </template>

    <DataTable
      v-else
      :columns="columns"
      :data="nodes"
      :loading="loading"
      row-key="id"
      selectable
      :selected-keys="selectedIds"
      :selection-label="(row: ClashNode) => row.name"
      :expandable-actions="false"
      @update:selected-keys="onSelectionChange"
    >
      <template #cell-name="{ row }">
        <div class="min-w-[11rem] max-w-[14rem]">
          <div class="flex items-center gap-1.5">
            <span class="truncate font-medium text-gray-900 dark:text-white" :title="row.name">{{ row.name }}</span>
            <span :class="CLASH_TYPE_BADGE_CLASS">{{ row.type }}</span>
            <span
              v-if="row.hidden"
              :class="CLASH_HIDDEN_BADGE_CLASS"
              :title="t('admin.clash.nodes.hiddenHint')"
              data-testid="clash-node-hidden-badge"
            >
              <Icon name="eyeOff" size="xs" />
              {{ t('admin.clash.nodes.hiddenTag') }}
            </span>
          </div>
          <code
            class="mt-0.5 block truncate font-mono text-xs text-gray-600 dark:text-gray-400"
            :title="`${row.server}:${row.server_port}`"
            data-testid="clash-node-server"
          >{{ row.server }}:{{ row.server_port }}</code>
          <div class="mt-0.5 flex items-center gap-1.5 text-xs text-gray-400 dark:text-dark-500">
            <span class="truncate" :title="row.profile_name">{{ row.profile_name }}</span>
            <span
              v-if="row.listen_port"
              class="flex-shrink-0 font-mono"
              :title="t('admin.clash.nodes.columns.listenPort')"
              data-testid="clash-node-listen-port"
            >· {{ t('admin.clash.nodes.listenPort', { port: row.listen_port }) }}</span>
          </div>
        </div>
      </template>

      <template #cell-status="{ row }">
        <div class="flex max-w-[10.5rem] flex-col items-start gap-1 whitespace-normal">
          <span :class="statusPillClass(row.status)" data-testid="clash-node-status">
            <span class="h-1.5 w-1.5 rounded-full" :class="statusDotClass(row.status)" aria-hidden="true"></span>
            {{ statusLabel(row.status) }}
          </span>
          <span class="flex flex-wrap items-center gap-x-1.5 text-xs" :title="healthTitle(row)" data-testid="clash-node-health">
            <span class="h-2 w-2 flex-shrink-0 rounded-full" :class="healthDotClass(row)" aria-hidden="true"></span>
            <span :class="healthTextClass(row)">{{ healthLabel(row) }}</span>
            <span
              v-if="row.consecutive_failures > 0"
              :class="row.health_status === 'unhealthy' ? 'text-red-600 dark:text-red-400' : 'text-amber-600 dark:text-amber-400'"
            >
              · {{ t('admin.clash.nodes.consecutiveFailures', { count: row.consecutive_failures }) }}
            </span>
            <span v-else-if="row.last_checked_at" class="text-gray-400 dark:text-dark-500">
              · {{ formatRelativeTime(row.last_checked_at) }}
            </span>
          </span>
          <span
            v-if="row.status === 'missing' && row.missing_since"
            class="text-xs text-gray-500 dark:text-dark-400"
            :title="formatDateTime(row.missing_since)"
          >{{ t('admin.clash.nodes.missingSince', { time: formatRelativeTime(row.missing_since) }) }}</span>
          <span
            v-if="row.status !== 'active' && row.status_reason"
            class="line-clamp-2 break-words text-xs"
            :class="row.status === 'invalid' ? 'text-red-600 dark:text-red-400' : 'text-gray-500 dark:text-dark-400'"
            :title="row.status_reason"
          >{{ localizeClashUnavailableReason(row.status_reason, t) }}</span>
          <span
            v-if="checkError(row)"
            class="line-clamp-2 break-words text-xs"
            :class="row.health_status === 'unhealthy' ? 'text-red-600 dark:text-red-400' : 'text-amber-700 dark:text-amber-300'"
            :title="checkError(row)"
            data-testid="clash-node-check-error"
          >{{ checkError(row) }}</span>
        </div>
      </template>

      <template #cell-exit="{ row }">
        <div class="min-w-[9rem] max-w-[12rem] whitespace-normal" data-testid="clash-node-exit">
          <div v-if="row.exit_ip" class="flex items-center gap-1.5">
            <CountryFlag :code="row.exit_country_code" :label="row.exit_country" />
            <span class="font-mono text-xs text-gray-900 dark:text-gray-100">{{ row.exit_ip }}</span>
          </div>
          <div
            v-else-if="exitFailed(row)"
            class="text-xs text-amber-600 dark:text-amber-400"
            :title="t('admin.clash.nodes.checkedAt', { time: formatDateTime(row.exit_checked_at) })"
            data-testid="clash-node-exit-failed"
          >{{ t('admin.clash.nodes.exitFailed') }}</div>
          <div v-else class="text-xs italic text-gray-400 dark:text-dark-500">{{ t('admin.clash.nodes.exitUnprobed') }}</div>
          <div v-if="exitLocation(row)" class="mt-0.5 truncate text-xs text-gray-500 dark:text-dark-400">{{ exitLocation(row) }}</div>
          <div
            v-if="row.exit_status === 'changed'"
            class="mt-1 flex flex-wrap items-center gap-x-1.5 gap-y-1 rounded-md bg-amber-50 px-1.5 py-1 text-xs text-amber-800 dark:bg-amber-500/10 dark:text-amber-300"
            data-testid="clash-node-exit-changed"
          >
            <Icon name="exclamationTriangle" size="xs" class="flex-shrink-0" />
            <span>{{ t('admin.clash.nodes.pendingExit', { ip: row.exit_pending_ip || '-' }) }}</span>
            <button
              type="button"
              class="font-semibold underline decoration-dotted underline-offset-2 hover:text-amber-900 dark:hover:text-amber-200"
              data-testid="clash-node-accept-exit"
              @click="pendingAccept = row"
            >
              {{ t('admin.clash.actions.acceptExit') }}
            </button>
          </div>
          <div v-else-if="row.exit_status === 'stale'" class="mt-0.5 text-xs text-amber-600 dark:text-amber-400">
            {{ t('admin.clash.nodes.exitStale') }}
          </div>
          <div
            class="mt-1.5 grid w-max grid-cols-2 gap-1"
            role="group"
            :aria-label="t('admin.clash.nodes.columns.platforms')"
            data-testid="clash-node-platforms"
          >
            <span
              v-for="platform in CLASH_CHECK_PLATFORMS"
              :key="platform"
              :class="platformBadgeClass(row.platform_checks.results[platform])"
              :title="platformTitle(row, platform)"
            >{{ CLASH_PLATFORM_LABELS[platform] }}</span>
          </div>
        </div>
      </template>

      <template #cell-traffic="{ row }">
        <div class="min-w-[7rem] whitespace-nowrap text-xs" data-testid="clash-node-traffic">
          <div :title="t('admin.clash.nodes.traffic.split', {
            up: formatTrafficBytes(row.traffic?.today_upload_bytes),
            down: formatTrafficBytes(row.traffic?.today_download_bytes)
          })">
            <span class="text-gray-500 dark:text-dark-400">{{ t('admin.clash.nodes.traffic.today') }}</span>
            <span class="ml-1 font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ formatTrafficBytes(clashTrafficToday(row.traffic)) }}</span>
          </div>
          <div class="mt-0.5 text-gray-500 dark:text-dark-400" :title="t('admin.clash.nodes.traffic.approxHint')">
            {{ t('admin.clash.nodes.traffic.totalValue', { value: formatTrafficBytes(clashTrafficTotal(row.traffic)) }) }}
          </div>
          <div v-if="(row.traffic?.connections ?? 0) > 0" class="mt-0.5 tabular-nums text-primary-700 dark:text-primary-300">
            {{ formatTrafficRate(row.traffic?.download_rate) }} · {{ t('admin.clash.nodes.traffic.connections', { count: row.traffic?.connections ?? 0 }) }}
          </div>
        </div>
      </template>

      <template #cell-accounts="{ row }">
        <button
          type="button"
          class="flex max-w-[10rem] items-center gap-1 rounded-md px-1 py-0.5 text-left transition-colors hover:bg-gray-100 dark:hover:bg-dark-700"
          :title="row.accounts.length > 0 ? `${accountNames(row)}\n${t('admin.clash.nodes.card.manageBindings')}` : t('admin.clash.nodes.card.manageBindings')"
          data-testid="clash-node-accounts"
          @click="openBindings(row)"
        >
          <template v-if="row.accounts.length > 0">
            <span
              class="inline-block max-w-[8rem] truncate rounded bg-gray-100 px-1.5 py-0.5 align-middle text-xs text-gray-700 dark:bg-dark-700 dark:text-gray-300"
            >{{ row.accounts[0].name }}<template v-if="row.accounts[0].is_shadow"> · {{ t('admin.clash.nodes.shadowTag') }}</template></span>
            <span v-if="row.accounts.length > 1" class="flex-shrink-0 text-xs text-gray-500 dark:text-dark-400">+{{ row.accounts.length - 1 }}</span>
          </template>
          <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
        </button>
      </template>

      <template #cell-actions="{ row }">
        <div class="flex items-center gap-0.5">
          <button
            type="button"
            class="row-action px-1.5 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="readonly || busyIds.has(row.id) || row.status === 'invalid'"
            :title="t('admin.clash.actions.testLatency')"
            :aria-label="t('admin.clash.actions.testLatency')"
            data-testid="clash-node-latency"
            @click="testOne(row)"
          >
            <Icon name="bolt" size="sm" :class="busyIds.has(row.id) ? 'animate-pulse' : ''" />
          </button>
          <button
            type="button"
            class="row-action px-1.5"
            :title="t('admin.clash.nodes.card.manageBindings')"
            :aria-label="t('admin.clash.nodes.card.manageBindings')"
            data-testid="clash-node-bindings"
            @click="openBindings(row)"
          >
            <Icon name="link" size="sm" />
          </button>
          <button
            v-if="row.hidden"
            type="button"
            class="row-action px-1.5 hover:!bg-emerald-50 hover:!text-emerald-700 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:!bg-emerald-500/10 dark:hover:!text-emerald-300"
            :disabled="busyIds.has(row.id) || nodeActionRunning"
            :title="t('admin.clash.actions.unhide')"
            :aria-label="t('admin.clash.actions.unhide')"
            data-testid="clash-node-unhide"
            @click="requestNodeAction('unhide', [row.id])"
          >
            <Icon name="eye" size="sm" />
          </button>
          <template v-else>
            <button
              v-if="row.status === 'disabled'"
              type="button"
              class="row-action px-1.5 hover:!bg-emerald-50 hover:!text-emerald-700 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:!bg-emerald-500/10 dark:hover:!text-emerald-300"
              :disabled="busyIds.has(row.id)"
              :title="t('admin.clash.actions.enable')"
              :aria-label="t('admin.clash.actions.enable')"
              data-testid="clash-node-enable"
              @click="setNodeEnabled(row, true)"
            >
              <Icon name="checkCircle" size="sm" />
            </button>
            <button
              v-else-if="row.status === 'active'"
              type="button"
              class="row-action px-1.5 hover:!bg-amber-50 hover:!text-amber-700 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:!bg-amber-500/10 dark:hover:!text-amber-300"
              :disabled="busyIds.has(row.id)"
              :title="t('admin.clash.actions.disable')"
              :aria-label="t('admin.clash.actions.disable')"
              data-testid="clash-node-disable"
              @click="pendingDisable = row"
            >
              <Icon name="ban" size="sm" />
            </button>
            <button
              type="button"
              class="row-action px-1.5 disabled:cursor-not-allowed disabled:opacity-50"
              :disabled="busyIds.has(row.id) || nodeActionRunning"
              :title="t('admin.clash.actions.hide')"
              :aria-label="t('admin.clash.actions.hide')"
              data-testid="clash-node-hide"
              @click="requestNodeAction('hide', [row.id])"
            >
              <Icon name="eyeOff" size="sm" />
            </button>
          </template>
        </div>
      </template>

      <template #empty>
        <EmptyState :title="t('admin.clash.nodes.empty')" :description="t('admin.clash.nodes.emptyHint')" />
      </template>
    </DataTable>

    <Pagination
      v-if="pagination.total > 0"
      :page="pagination.page"
      :total="pagination.total"
      :page-size="pagination.page_size"
      @update:page="handlePageChange"
      @update:pageSize="handlePageSizeChange"
    />

    <ConfirmDialog
      :show="pendingDisable !== null"
      :title="t('admin.clash.nodes.disableConfirm.title')"
      :message="t('admin.clash.nodes.disableConfirm.message', { name: pendingDisable?.name ?? '' })"
      :confirm-text="t('admin.clash.actions.disable')"
      danger
      @confirm="confirmDisable"
      @cancel="pendingDisable = null"
    >
      <div
        v-if="pendingDisable && pendingDisable.accounts.length > 0"
        class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
        data-testid="clash-node-disable-bound"
      >
        <p>{{ t('admin.clash.nodes.disableConfirm.bound', { count: pendingDisable.accounts.length }) }}</p>
        <div class="mt-2 flex flex-wrap gap-1">
          <span
            v-for="account in pendingDisable.accounts"
            :key="account.id"
            class="rounded bg-white/70 px-1.5 py-0.5 text-xs font-medium dark:bg-dark-800/60"
          >{{ account.name }}</span>
        </div>
      </div>
    </ConfirmDialog>

    <ConfirmDialog
      :show="pendingAction !== null"
      :title="pendingActionTitle"
      :message="pendingActionMessage"
      :confirm-text="pendingAction ? nodeActionLabel(pendingAction.action) : ''"
      danger
      @confirm="confirmNodeAction"
      @cancel="pendingAction = null"
    >
      <div
        v-if="pendingAction && pendingAction.accounts.length > 0"
        class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
        data-testid="clash-node-action-bound"
      >
        <p>{{ t(pendingAction.ids.length === 1 ? 'admin.clash.nodes.actionConfirm.boundOne' : 'admin.clash.nodes.actionConfirm.bound', { count: pendingAction.accounts.length }) }}</p>
        <div class="mt-2 flex flex-wrap gap-1">
          <span
            v-for="account in pendingAction.accounts"
            :key="account.id"
            class="rounded bg-white/70 px-1.5 py-0.5 text-xs font-medium dark:bg-dark-800/60"
          >{{ account.name }}</span>
        </div>
      </div>
    </ConfirmDialog>

    <ConfirmDialog
      :show="pendingAccept !== null"
      :title="t('admin.clash.nodes.acceptConfirm.title')"
      :message="t('admin.clash.nodes.acceptConfirm.message', {
        name: pendingAccept?.name ?? '',
        from: pendingAccept?.exit_ip || '-',
        to: pendingAccept?.exit_pending_ip || '-'
      })"
      :confirm-text="t('admin.clash.actions.acceptExit')"
      @confirm="confirmAcceptExit"
      @cancel="pendingAccept = null"
    />

    <ClashNodeBindingsDialog
      :show="bindingNode !== null"
      :node="bindingNode"
      :readonly="readonly"
      @close="bindingNode = null"
      @changed="handleBindingsChanged"
    />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watchEffect } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import CountryFlag from '@/components/common/CountryFlag.vue'
import AutoRefreshButton from '@/components/common/AutoRefreshButton.vue'
import Icon from '@/components/icons/Icon.vue'
import ClashNodeCard from './ClashNodeCard.vue'
import ClashNodeBindingsDialog from './ClashNodeBindingsDialog.vue'
import type { Column } from '@/components/common/types'
import type {
  ClashBoundAccount,
  ClashExitProbeResult,
  ClashHealthStatus,
  ClashLatencyResult,
  ClashNode,
  ClashNodeAction,
  ClashNodeListFilters,
  ClashNodeSort,
  ClashNodeStatus,
  ClashNodeVisibility,
  ClashProfile
} from '@/types'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { useAutoRefresh } from '@/composables/useAutoRefresh'
import { useClashBatchTest, type ClashBatchKind, type ClashBatchProgress } from '@/composables/useClashBatchTest'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  clashErrorMessage,
  clashTrafficToday,
  clashTrafficTotal,
  formatTrafficBytes,
  formatTrafficRate,
  localizeClashUnavailableReason
} from '@/utils/clash'
import { formatDateTime, formatRelativeTime } from '@/utils/format'
import {
  CLASH_CHECK_PLATFORMS,
  CLASH_PLATFORM_LABELS,
  CLASH_HIDDEN_BADGE_CLASS,
  CLASH_TYPE_BADGE_CLASS,
  useClashNodeDisplay
} from './useClashNodeDisplay'

type BoundFilter = 'all' | 'bound' | 'unbound'
type ViewMode = 'card' | 'table'

const VIEW_MODE_KEY = 'clash-nodes-view-mode'

const props = withDefaults(
  defineProps<{
    profiles: ClashProfile[]
    readonly?: boolean
    /** Per-exit account limit from the pool settings (null while unknown). */
    maxAccountsPerExit?: number | null
  }>(),
  { readonly: false, maxAccountsPerExit: null }
)

const emit = defineEmits<{ (e: 'changed'): void }>()

const { t } = useI18n()
const appStore = useAppStore()
const {
  statusLabel,
  statusPillClass,
  statusDotClass,
  healthLabel,
  healthDotClass,
  healthTextClass,
  healthTitle,
  checkError,
  exitFailed,
  exitLocation,
  platformBadgeClass,
  platformTitle,
  accountNames
} = useClashNodeDisplay()

const rootRef = ref<HTMLElement | null>(null)
const nodes = ref<ClashNode[]>([])
const loading = ref(false)
const loadError = ref('')
const selectedIds = ref<number[]>([])
const busyIds = ref(new Set<number>())
const pendingDisable = ref<ClashNode | null>(null)
const pendingAccept = ref<ClashNode | null>(null)
/** Hiding and bulk disabling wait for confirmation, listing the accounts that will be paused. */
const pendingAction = ref<{ action: ClashNodeAction; ids: number[]; name: string; accounts: ClashBoundAccount[] } | null>(null)
const nodeActionRunning = ref(false)
/** Nodes seen on any page, so a selection spanning pages can still name its bound accounts. */
const knownNodes = new Map<number, ClashNode>()
const bindingNode = ref<ClashNode | null>(null)
const batchMenuOpen = ref(false)
const batchMenuRef = ref<HTMLElement | null>(null)
const selectPageRef = ref<HTMLInputElement | null>(null)

function readViewMode(): ViewMode {
  try {
    return localStorage.getItem(VIEW_MODE_KEY) === 'table' ? 'table' : 'card'
  } catch {
    return 'card'
  }
}

const viewMode = ref<ViewMode>(readViewMode())

function setViewMode(mode: ViewMode) {
  viewMode.value = mode
  try {
    localStorage.setItem(VIEW_MODE_KEY, mode)
  } catch {
    // Private mode: the choice just isn't remembered.
  }
}

const filters = reactive({
  search: '',
  profileId: null as number | null,
  status: '' as '' | ClashNodeStatus,
  health: '' as '' | ClashHealthStatus,
  bound: 'all' as BoundFilter,
  sort: '' as ClashNodeSort,
  visibility: 'visible' as ClashNodeVisibility
})

const pagination = reactive({
  page: 1,
  page_size: getPersistedPageSize(),
  total: 0,
  pages: 0
})

const viewOptions = computed(() => [
  { value: 'card' as ViewMode, icon: 'grid' as const, title: t('admin.clash.nodes.view.card') },
  { value: 'table' as ViewMode, icon: 'menu' as const, title: t('admin.clash.nodes.view.table') }
])

// Type, server:port and the listener port live in the node cell; health shares the status cell and
// platform reachability the exit cell, so the table fits beside the sidebar without scrolling.
const columns = computed<Column[]>(() => [
  { key: 'name', label: t('admin.clash.nodes.columns.name') },
  { key: 'status', label: t('admin.clash.nodes.columns.status') },
  { key: 'exit', label: t('admin.clash.nodes.columns.exit') },
  { key: 'traffic', label: t('admin.clash.nodes.columns.traffic') },
  { key: 'accounts', label: t('admin.clash.nodes.columns.accounts') },
  { key: 'actions', label: t('admin.clash.nodes.columns.actions') }
])

const profileOptions = computed(() => [
  { value: null, label: t('admin.clash.nodes.filters.allProfiles') },
  ...props.profiles.map((profile) => ({ value: profile.id, label: profile.name }))
])

const statusOptions = computed(() => [
  { value: '', label: t('admin.clash.nodes.filters.allStatus') },
  { value: 'active', label: t('admin.clash.nodeStatus.active') },
  { value: 'missing', label: t('admin.clash.nodeStatus.missing') },
  { value: 'disabled', label: t('admin.clash.nodeStatus.disabled') },
  { value: 'invalid', label: t('admin.clash.nodeStatus.invalid') }
])

const healthOptions = computed(() => [
  { value: '', label: t('admin.clash.nodes.filters.allHealth') },
  { value: 'healthy', label: t('admin.clash.health.healthy') },
  { value: 'unhealthy', label: t('admin.clash.health.unhealthy') },
  { value: 'unknown', label: t('admin.clash.health.unknown') }
])

const visibilityOptions = computed(() => [
  { value: 'visible', label: t('admin.clash.nodes.filters.visibilityVisible') },
  { value: 'hidden', label: t('admin.clash.nodes.filters.visibilityHidden') },
  { value: 'all', label: t('admin.clash.nodes.filters.visibilityAll') }
])

const boundOptions = computed(() => [
  { value: 'all' as BoundFilter, label: t('admin.clash.nodes.filters.boundAll') },
  { value: 'bound' as BoundFilter, label: t('admin.clash.nodes.filters.boundOnly') },
  { value: 'unbound' as BoundFilter, label: t('admin.clash.nodes.filters.unboundOnly') }
])

const sortOptions = computed(() => [
  { value: '', label: t('admin.clash.nodes.sort.default') },
  { value: 'latency', label: t('admin.clash.nodes.sort.latency') },
  { value: 'traffic_today', label: t('admin.clash.nodes.sort.trafficToday') },
  { value: 'traffic_total', label: t('admin.clash.nodes.sort.trafficTotal') },
  { value: 'name', label: t('admin.clash.nodes.sort.name') }
])

const batchMenuItems = computed(() => [
  { kind: 'latency' as ClashBatchKind, label: t('admin.clash.actions.testLatency'), hint: t('admin.clash.nodes.batch.latencyHint') },
  { kind: 'exit' as ClashBatchKind, label: t('admin.clash.actions.probeExit'), hint: t('admin.clash.nodes.batch.exitHint') },
  { kind: 'full' as ClashBatchKind, label: t('admin.clash.actions.fullCheck'), hint: t('admin.clash.nodes.batch.fullHint') }
])

// Hiding only applies to visible nodes and unhiding to hidden ones.
const bulkActionItems = computed(() => {
  const items: Array<{ action: ClashNodeAction; icon: 'checkCircle' | 'ban' | 'eyeOff' | 'eye'; label: string }> = [
    { action: 'enable', icon: 'checkCircle', label: t('admin.clash.actions.enable') },
    { action: 'disable', icon: 'ban', label: t('admin.clash.actions.disable') }
  ]
  if (filters.visibility !== 'hidden') items.push({ action: 'hide', icon: 'eyeOff', label: t('admin.clash.actions.hide') })
  if (filters.visibility !== 'visible') items.push({ action: 'unhide', icon: 'eye', label: t('admin.clash.actions.unhide') })
  return items
})

const selectedSet = computed(() => new Set(selectedIds.value))
const pageFullySelected = computed(() => nodes.value.length > 0 && nodes.value.every((node) => selectedSet.value.has(node.id)))
const pagePartiallySelected = computed(() => !pageFullySelected.value && nodes.value.some((node) => selectedSet.value.has(node.id)))

watchEffect(() => {
  if (selectPageRef.value) selectPageRef.value.indeterminate = pagePartiallySelected.value
})

const describeError = (error: unknown, fallbackKey: string) =>
  clashErrorMessage(error, t) ?? extractApiErrorMessage(error, t(fallbackKey))

const buildFilters = (): ClashNodeListFilters => ({
  profile_id: filters.profileId ?? undefined,
  status: filters.status || undefined,
  health: filters.health || undefined,
  bound: filters.bound === 'all' ? undefined : filters.bound === 'bound',
  search: filters.search.trim() || undefined,
  sort: filters.sort || undefined,
  visibility: filters.visibility
})

let abortController: AbortController | null = null

const isAbortError = (error: unknown) => {
  if (!error || typeof error !== 'object') return false
  const maybe = error as { name?: string; code?: string }
  return maybe.name === 'AbortError' || maybe.code === 'ERR_CANCELED' || maybe.name === 'CanceledError'
}

/** silent keeps the current rows on screen (auto refresh, after tests). */
async function load(options: { silent?: boolean } = {}) {
  abortController?.abort()
  const controller = new AbortController()
  abortController = controller
  if (!options.silent) loading.value = true
  try {
    const response = await adminAPI.clash.listNodes(pagination.page, pagination.page_size, buildFilters(), {
      signal: controller.signal
    })
    if (controller.signal.aborted || abortController !== controller) return
    nodes.value = Array.isArray(response?.items) ? response.items : []
    for (const node of nodes.value) knownNodes.set(node.id, node)
    pagination.total = response?.total ?? 0
    pagination.pages = response?.pages ?? 0
    loadError.value = ''
    if (bindingNode.value) {
      // Hand the reloaded node to the open bindings dialog.
      bindingNode.value = nodes.value.find((node) => node.id === bindingNode.value?.id) ?? bindingNode.value
    }
  } catch (error) {
    if (isAbortError(error)) return
    loadError.value = describeError(error, 'admin.clash.nodes.loadFailed')
  } finally {
    if (abortController === controller) {
      loading.value = false
      abortController = null
      autoRefresh.resetCountdown()
    }
  }
}

const autoRefresh = useAutoRefresh({
  storageKey: 'clash-nodes-auto-refresh',
  intervals: [10, 30, 60] as const,
  defaultInterval: 30,
  onRefresh: () => load({ silent: true }),
  shouldPause: () => document.hidden || loading.value || batch.running.value || bindingNode.value !== null
})

function applyFilters() {
  pagination.page = 1
  selectedIds.value = []
  void load()
}

let searchTimer: ReturnType<typeof setTimeout> | undefined
function onSearchInput() {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(applyFilters, 300)
}

function clearSearch() {
  filters.search = ''
  applyFilters()
}

function handlePageChange(page: number) {
  pagination.page = page
  void load()
}

function handlePageSizeChange(pageSize: number) {
  pagination.page_size = pageSize
  pagination.page = 1
  void load()
}

function onSelectionChange(keys: Array<string | number>) {
  selectedIds.value = keys.map((key) => Number(key)).filter((id) => Number.isFinite(id))
}

function toggleSelected(id: number) {
  selectedIds.value = selectedSet.value.has(id)
    ? selectedIds.value.filter((item) => item !== id)
    : [...selectedIds.value, id]
}

function togglePageSelection() {
  const pageIds = nodes.value.map((node) => node.id)
  if (pageFullySelected.value) {
    const onPage = new Set(pageIds)
    selectedIds.value = selectedIds.value.filter((id) => !onPage.has(id))
  } else {
    selectedIds.value = [...new Set([...selectedIds.value, ...pageIds])]
  }
}

function markBusy(ids: number[], busy: boolean) {
  const next = new Set(busyIds.value)
  for (const id of ids) {
    if (busy) next.add(id)
    else next.delete(id)
  }
  busyIds.value = next
}

function findNode(id: number) {
  return nodes.value.find((node) => node.id === id)
}

/** Updates the visible rows as results arrive, before the final reload. */
function applyLatencyResults(results: ClashLatencyResult[]) {
  const now = new Date().toISOString()
  for (const result of results) {
    const node = findNode(result.node_id)
    if (!node) continue
    node.health_status = result.health_status
    if (result.success && typeof result.latency_ms === 'number') node.latency_ms = result.latency_ms
    node.last_checked_at = now
    node.last_check_error = result.error ?? ''
  }
}

function applyExitResults(results: ClashExitProbeResult[]) {
  const now = new Date().toISOString()
  for (const result of results) {
    const node = findNode(result.node_id)
    if (!node) continue
    node.exit_status = result.exit_status
    node.exit_checked_at = now
    if (!result.success || !result.exit_ip) continue
    if (result.exit_status === 'changed') {
      node.exit_pending_ip = result.exit_ip
    } else {
      node.exit_ip = result.exit_ip
      if (result.country) node.exit_country = result.country
    }
  }
}

const batch = useClashBatchTest({ onLatency: applyLatencyResults, onExit: applyExitResults })
const runningKind = computed(() => (batch.running.value ? batch.progress.value?.kind ?? null : null))
const batchPercent = computed(() => {
  const progress = batch.progress.value
  if (!progress || progress.total === 0) return 0
  return Math.round((progress.done / progress.total) * 100)
})

function batchKindLabel(kind: ClashBatchKind) {
  switch (kind) {
    case 'latency':
      return t('admin.clash.actions.testLatency')
    case 'exit':
      return t('admin.clash.actions.probeExit')
    default:
      return t('admin.clash.actions.fullCheck')
  }
}

function notifyBatch(result: ClashBatchProgress) {
  const { success, failed, changed } = result
  if (result.cancelled) {
    appStore.showInfo(t('admin.clash.nodes.batch.stopped', { done: result.done, total: result.total }))
    return
  }
  if (result.kind === 'latency') {
    const message = t('admin.clash.nodes.latencyDone', { success, failed })
    if (failed === 0) appStore.showSuccess(message)
    else appStore.showWarning(message)
    return
  }
  const full = result.kind === 'full'
  if (changed > 0) {
    appStore.showWarning(full
      ? t('admin.clash.nodes.batch.fullChanged', { success, failed, changed })
      : t('admin.clash.nodes.probeChanged', { success, failed, changed }))
    return
  }
  const message = full
    ? t('admin.clash.nodes.batch.fullDone', { success, failed })
    : t('admin.clash.nodes.probeDone', { success, failed })
  if (failed > 0) appStore.showWarning(message)
  else appStore.showSuccess(message)
}

/** Tests the selected nodes, or every node matching the filters (all pages). */
async function startBatch(kind: ClashBatchKind, scope: 'selected' | 'filtered') {
  batchMenuOpen.value = false
  if (props.readonly || batch.running.value) return
  let ids: number[]
  if (scope === 'selected') {
    ids = [...selectedIds.value]
  } else {
    try {
      ids = await adminAPI.clash.listNodeIds(buildFilters(), { live: true })
    } catch (error) {
      appStore.showError(describeError(error, 'admin.clash.nodes.batch.loadIdsFailed'))
      return
    }
  }
  if (ids.length === 0) {
    appStore.showInfo(t('admin.clash.nodes.batch.empty'))
    return
  }
  markBusy(ids, true)
  let result: ClashBatchProgress | null = null
  try {
    result = await batch.run(kind, ids)
  } finally {
    markBusy(ids, false)
  }
  if (!result) return
  notifyBatch(result)
  await load({ silent: true })
  emit('changed')
}

async function testOne(node: ClashNode) {
  if (props.readonly || busyIds.value.has(node.id)) return
  markBusy([node.id], true)
  try {
    const results = await adminAPI.clash.testNodesLatency({ node_ids: [node.id] })
    applyLatencyResults(results)
    const success = results.filter((result) => result.success).length
    const failed = results.length - success
    // One node: say why it failed instead of only counting.
    const reason = results.length === 1 && !results[0].success ? results[0].error?.trim() : ''
    const message = t('admin.clash.nodes.latencyDone', { success, failed })
    if (reason) appStore.showError(t('admin.clash.nodes.latencyFailedDetail', { error: reason }))
    else if (failed === 0) appStore.showSuccess(message)
    else appStore.showWarning(message)
    await load({ silent: true })
    emit('changed')
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.nodes.latencyFailed'))
  } finally {
    markBusy([node.id], false)
  }
}

async function probeOne(node: ClashNode) {
  if (props.readonly || busyIds.value.has(node.id)) return
  markBusy([node.id], true)
  try {
    const results = await adminAPI.clash.probeNodesExit({ node_ids: [node.id] })
    applyExitResults(results)
    const success = results.filter((result) => result.success).length
    const failed = results.length - success
    const changed = results.filter((result) => result.exit_status === 'changed').length
    const reason = results.length === 1 && !results[0].success ? results[0].error?.trim() : ''
    if (reason) appStore.showError(t('admin.clash.nodes.probeFailedDetail', { error: reason }))
    else if (changed > 0) appStore.showWarning(t('admin.clash.nodes.probeChanged', { success, failed, changed }))
    else if (failed > 0) appStore.showWarning(t('admin.clash.nodes.probeDone', { success, failed }))
    else appStore.showSuccess(t('admin.clash.nodes.probeDone', { success, failed }))
    await load({ silent: true })
    emit('changed')
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.nodes.probeFailed'))
  } finally {
    markBusy([node.id], false)
  }
}

async function setNodeEnabled(node: ClashNode, enabled: boolean) {
  markBusy([node.id], true)
  try {
    if (enabled) await adminAPI.clash.enableNode(node.id)
    else await adminAPI.clash.disableNode(node.id)
    appStore.showSuccess(enabled ? t('admin.clash.nodes.enabledToast') : t('admin.clash.nodes.disabledToast'))
    await load()
    emit('changed')
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.nodes.toggleFailed'))
  } finally {
    markBusy([node.id], false)
  }
}

async function confirmDisable() {
  const node = pendingDisable.value
  pendingDisable.value = null
  if (node) await setNodeEnabled(node, false)
}

function nodeActionLabel(action: ClashNodeAction) {
  return t(`admin.clash.actions.${action}`)
}

/** Bound accounts that go offline with the nodes (only live nodes still carry traffic). */
function affectedAccounts(ids: number[]): ClashBoundAccount[] {
  const accounts = new Map<number, ClashBoundAccount>()
  for (const id of ids) {
    const node = knownNodes.get(id)
    if (node?.status !== 'active') continue
    for (const account of node.accounts) accounts.set(account.id, account)
  }
  return [...accounts.values()]
}

const pendingActionTitle = computed(() => {
  const pending = pendingAction.value
  if (!pending) return ''
  const base = `admin.clash.nodes.actionConfirm.${pending.action}`
  return t(pending.ids.length === 1 ? `${base}.titleOne` : `${base}.title`)
})

const pendingActionMessage = computed(() => {
  const pending = pendingAction.value
  if (!pending) return ''
  const base = `admin.clash.nodes.actionConfirm.${pending.action}`
  return pending.ids.length === 1 && pending.name
    ? t(`${base}.messageOne`, { name: pending.name })
    : t(`${base}.messageMany`, { count: pending.ids.length })
})

/** Enabling and unhiding run at once; hiding and disabling take nodes offline, so they ask first. */
function requestNodeAction(action: ClashNodeAction, ids: number[]) {
  if (ids.length === 0 || nodeActionRunning.value) return
  if (action === 'enable' || action === 'unhide') {
    void runNodeAction(action, ids)
    return
  }
  const single = ids.length === 1 ? knownNodes.get(ids[0]) : undefined
  pendingAction.value = { action, ids: [...ids], name: single?.name ?? '', accounts: affectedAccounts(ids) }
}

async function confirmNodeAction() {
  const pending = pendingAction.value
  pendingAction.value = null
  if (pending) await runNodeAction(pending.action, pending.ids)
}

async function runNodeAction(action: ClashNodeAction, ids: number[]) {
  nodeActionRunning.value = true
  markBusy(ids, true)
  try {
    const result = await adminAPI.clash.updateNodes(action, ids)
    let message = t(`admin.clash.nodes.actionDone.${action}`, { count: result.updated })
    if (result.skipped > 0) message += t('admin.clash.nodes.actionSkipped', { count: result.skipped })
    if (result.updated > 0) appStore.showSuccess(message)
    else appStore.showInfo(message)
    const handled = new Set(ids)
    selectedIds.value = selectedIds.value.filter((id) => !handled.has(id))
    await load({ silent: true })
    // Hidden (or revealed) nodes leave the current view; do not strand the user on an empty page.
    if (nodes.value.length === 0 && pagination.page > 1) {
      pagination.page = Math.max(1, pagination.pages)
      await load({ silent: true })
    }
    emit('changed')
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.nodes.actionFailed'))
  } finally {
    markBusy(ids, false)
    nodeActionRunning.value = false
  }
}

async function confirmAcceptExit() {
  const node = pendingAccept.value
  pendingAccept.value = null
  if (!node) return
  markBusy([node.id], true)
  try {
    await adminAPI.clash.acceptNodeExit(node.id)
    appStore.showSuccess(t('admin.clash.nodes.acceptSuccess'))
    await load()
    emit('changed')
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.nodes.acceptFailed'))
  } finally {
    markBusy([node.id], false)
  }
}

function openBindings(node: ClashNode) {
  bindingNode.value = node
}

function handleBindingsChanged() {
  void load({ silent: true })
  emit('changed')
}

/** Filters the table to one subscription and brings the panel into view. */
function focusProfile(profileId: number | null) {
  filters.profileId = profileId
  applyFilters()
  rootRef.value?.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
}

function handleDocumentClick(event: MouseEvent) {
  if (batchMenuOpen.value && batchMenuRef.value && !batchMenuRef.value.contains(event.target as Node)) {
    batchMenuOpen.value = false
  }
}

onMounted(() => {
  void load()
  autoRefresh.setEnabled(autoRefresh.enabled.value)
  document.addEventListener('click', handleDocumentClick)
})

onUnmounted(() => {
  clearTimeout(searchTimer)
  abortController?.abort()
  batch.stop()
  document.removeEventListener('click', handleDocumentClick)
})

defineExpose({ reload: load, focusProfile })
</script>
