<template>
  <!-- 快捷操作：放在页面引导区右侧，主操作（创建密钥）在最右 -->
  <div class="flex flex-wrap items-center gap-2">
    <button
      v-if="canUseBatchImage"
      type="button"
      class="btn btn-secondary btn-sm"
      :title="t('dashboard.batchImageAgentDesc')"
      @click="router.push('/batch-image')"
    >
      <Icon name="sparkles" size="sm" />
      {{ t('dashboard.batchImageAgent') }}
    </button>
    <button
      type="button"
      class="btn btn-secondary btn-sm"
      :title="t('dashboard.addBalanceWithCode')"
      @click="router.push('/redeem')"
    >
      <Icon name="gift" size="sm" />
      {{ t('dashboard.redeemCode') }}
    </button>
    <button
      type="button"
      class="btn btn-secondary btn-sm"
      :title="t('dashboard.checkDetailedLogs')"
      @click="router.push('/usage')"
    >
      <Icon name="chart" size="sm" />
      {{ t('dashboard.viewUsage') }}
    </button>
    <button
      type="button"
      class="btn btn-primary btn-sm"
      :title="t('dashboard.generateNewKey')"
      @click="router.push('/keys')"
    >
      <Icon name="plus" size="sm" :stroke-width="2" />
      {{ t('dashboard.createApiKey') }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useBatchImageAccess } from '@/composables/useBatchImageAccess'
const router = useRouter()
const { t } = useI18n()
const { canUseBatchImage, refreshBatchImageAccess } = useBatchImageAccess()

onMounted(() => {
  void refreshBatchImageAccess()
})
</script>
