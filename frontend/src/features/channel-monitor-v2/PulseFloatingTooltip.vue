<template>
  <Teleport to="body">
    <div
      v-if="state.visible"
      :id="id"
      class="pulse-tooltip"
      :style="{ left: `${state.x}px`, top: `${state.y}px` }"
      role="tooltip"
    >
      <span
        v-for="(line, index) in state.lines"
        :key="`${index}:${line}`"
        class="pulse-tooltip-line"
        :class="index === 0 ? 'pulse-tooltip-title' : ''"
      >
        {{ line }}
      </span>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
/**
 * Hover/focus tooltip for RelayPulseMatrix, rendered through body so the matrix
 * viewport cannot clip it. The parent passes its reactive state object as-is:
 * only this component reads visible/x/y/lines, so pointer moves re-render this
 * tooltip instead of the whole matrix.
 */
export interface PulseTooltipState {
  visible: boolean
  x: number
  y: number
  lines: string[]
}

defineProps<{
  state: PulseTooltipState
  id?: string
}>()
</script>

<style scoped>
/* Dark variants are written as `.dark .x` (scoped → `.dark .x[data-v]`). `:global(.dark) .x` would
   compile to a bare global `.dark { ... }` rule that restyles <html> and never reaches the tooltip. */
.pulse-tooltip {
  pointer-events: none;
  position: fixed;
  z-index: 9999;
  min-width: 11.5rem;
  max-width: min(18rem, calc(100vw - 1.5rem));
  transform: translate(-50%, -100%);
  border-radius: 0.75rem;
  border: 1px solid rgb(229 231 235);
  background: rgb(255 255 255);
  padding: 0.5rem 0.625rem;
  box-shadow: 0 18px 40px -12px rgb(0 0 0 / 0.28);
  white-space: nowrap;
}
.dark .pulse-tooltip {
  border-color: rgb(55 65 81);
  background: rgb(17 24 39);
  color: rgb(229 231 235);
}
.pulse-tooltip-line {
  display: block;
  font-size: 11px;
  line-height: 1.45;
  color: rgb(75 85 99);
}
.dark .pulse-tooltip-line {
  color: rgb(209 213 219);
}
.pulse-tooltip-title {
  margin-bottom: 0.2rem;
  font-weight: 600;
  color: rgb(17 24 39);
}
.dark .pulse-tooltip-title {
  color: rgb(243 244 246);
}
</style>
