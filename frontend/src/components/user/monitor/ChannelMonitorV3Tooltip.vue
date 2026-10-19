<template>
  <Teleport to="body">
    <Transition name="v3-timeline-tooltip">
      <div
        v-if="state.visible"
        :id="id"
        class="v3-timeline-tooltip"
        :class="`is-${state.align}`"
        role="tooltip"
        :style="{ left: `${state.left}px`, top: `${state.top}px` }"
      >
        {{ state.text }}
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
/**
 * The single floating tooltip for all V3 timelines. The page passes its reactive state
 * object as-is: only this component reads visible/left/top/text, so hover changes
 * re-render this tooltip and nothing else (same pattern as PulseFloatingTooltip).
 */
import type { V3TooltipState } from './v3TimelineTooltip'

defineProps<{
  state: V3TooltipState
  id?: string
}>()
</script>

<style scoped>
.v3-timeline-tooltip {
  --tooltip-x: -50%;
  position: fixed;
  z-index: 50;
  width: max-content;
  max-width: min(280px, calc(100vw - 32px));
  transform: translateX(var(--tooltip-x)) translateY(-100%);
  border: 1px solid rgb(255 255 255 / 0.84);
  border-radius: 9px;
  background: rgb(15 23 42 / 0.92);
  padding: 6px 9px;
  color: rgb(248 250 252);
  font-size: 10px;
  font-weight: 600;
  line-height: 1.35;
  letter-spacing: 0;
  white-space: normal;
  box-shadow: 0 10px 24px rgb(15 23 42 / 0.2);
  pointer-events: none;
}

.v3-timeline-tooltip.is-start {
  --tooltip-x: 0%;
}

.v3-timeline-tooltip.is-end {
  --tooltip-x: -100%;
}

.v3-timeline-tooltip::after {
  position: absolute;
  left: 50%;
  bottom: -4px;
  width: 7px;
  height: 7px;
  transform: translateX(-50%) rotate(45deg);
  border-right: 1px solid rgb(255 255 255 / 0.84);
  border-bottom: 1px solid rgb(255 255 255 / 0.84);
  background: rgb(15 23 42 / 0.92);
  content: '';
}

.v3-timeline-tooltip-enter-active,
.v3-timeline-tooltip-leave-active {
  transition: opacity 100ms ease, transform 120ms cubic-bezier(0.22, 1, 0.36, 1);
}

.v3-timeline-tooltip-enter-from,
.v3-timeline-tooltip-leave-to {
  opacity: 0;
  transform: translateX(var(--tooltip-x)) translateY(calc(-100% + 3px)) scale(0.96);
}

@media (prefers-reduced-motion: reduce) {
  .v3-timeline-tooltip-enter-active,
  .v3-timeline-tooltip-leave-active {
    transition: none;
  }
}
</style>
