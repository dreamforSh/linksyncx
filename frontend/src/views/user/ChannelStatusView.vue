<template>
  <AppLayout v-if="!modeKnown">
    <div class="flex items-center justify-center py-24" data-testid="channel-status-mode-loading">
      <LoadingSpinner />
    </div>
  </AppLayout>
  <ChannelStatusV1View v-else-if="isV1" />
  <ChannelStatusV2View v-else />
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useAppStore } from '@/stores/app'
import { isChannelMonitorV1Mode } from '@/utils/featureFlags'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import ChannelStatusV1View from './ChannelStatusV1View.vue'
import ChannelStatusV2View from './ChannelStatusV2View.vue'

const appStore = useAppStore()

// The mode comes from public settings. Without SSR injection they are still in flight on a
// cold load, where getChannelMonitorMode() reports its v1 fallback: V1 would mount (and fire
// its list request) before the switch to V2. Render neither until the fetch settles; a failed
// fetch keeps the documented v1 fallback.
const modeKnown = ref(appStore.cachedPublicSettings !== null)
if (!modeKnown.value) {
  void appStore.fetchPublicSettings().finally(() => {
    modeKnown.value = true
  })
}

const isV1 = computed(() => isChannelMonitorV1Mode())
</script>
