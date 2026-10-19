<template>
  <span v-if="seconds !== null" class="tabular-nums" data-testid="channel-status-countdown">{{ t('monitorCommon.nextUpdateIn', { n: seconds }) }}</span>
</template>

<script setup lang="ts">
/**
 * Owns the once-per-second tick so only this text node updates; the page and its cards
 * never re-render for the countdown. Seconds are derived from the same deadline that
 * arms the refresh timer, so the two cannot drift (e.g. while the tab is hidden).
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  /** Epoch ms of the next scheduled refresh; null while a refresh is in flight. */
  deadline: number | null
}>()

const { t } = useI18n()
const now = ref(Date.now())
const seconds = computed(() => (
  props.deadline == null ? null : Math.max(0, Math.ceil((props.deadline - now.value) / 1000))
))

let timer: number | null = null

function tick() {
  // Nothing to paint in a background tab; the first visible tick catches up from the deadline.
  if (typeof document !== 'undefined' && document.hidden) return
  now.value = Date.now()
}

watch(() => props.deadline, () => { now.value = Date.now() })
onMounted(() => { timer = window.setInterval(tick, 1000) })
onBeforeUnmount(() => { if (timer != null) window.clearInterval(timer) })
</script>
