<script setup lang="ts">
import { onMounted, onScopeDispose, watch, ref, computed, shallowRef } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import {
  TitleComponent,
  TooltipComponent,
  GridComponent,
  LegendComponent,
} from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { MetricsQueryResponse, MetricsResultValue } from '../api'

echarts.use([
  TitleComponent,
  TooltipComponent,
  GridComponent,
  LegendComponent,
  LineChart,
  CanvasRenderer,
])

const props = defineProps<{
  title: string
  result: MetricsQueryResponse | null
  loading: boolean
  error: string | null
  /** How to label each series. Defaults to JSON of all labels. */
  seriesLabel?: (m: Record<string, string>) => string
  /** Y-axis unit suffix, e.g. "%" or " B". */
  yUnit?: string
}>()

const containerRef = ref<HTMLDivElement | null>(null)
const chart = shallowRef<echarts.ECharts | null>(null)

const seriesLabel = props.seriesLabel ?? ((m) => JSON.stringify(m))

const option = computed(() => {
  const series = (props.result?.data?.result ?? []).map((r: MetricsResultValue) => ({
    name: seriesLabel(r.metric),
    type: 'line' as const,
    showSymbol: false,
    smooth: false,
    data: r.values.map(([ts, v]) => [ts * 1000, Number(v)]),
  }))
  return {
    title: { text: props.title, left: 0, textStyle: { fontSize: 14, fontWeight: 600 as const } },
    tooltip: { trigger: 'axis' as const },
    legend: { type: 'scroll' as const, top: 24, textStyle: { fontSize: 11 } },
    grid: { left: 50, right: 16, top: 54, bottom: 24 },
    xAxis: { type: 'time' as const },
    yAxis: {
      type: 'value' as const,
      axisLabel: {
        formatter: (v: number) => `${v.toFixed(1)}${props.yUnit ?? ''}`,
      },
    },
    series,
  }
})

onMounted(() => {
  if (!containerRef.value) return
  chart.value = echarts.init(containerRef.value)
  chart.value.setOption(option.value)
  window.addEventListener('resize', resize)
})

onScopeDispose(() => {
  window.removeEventListener('resize', resize)
  chart.value?.dispose()
})

function resize() {
  chart.value?.resize()
}

watch(option, (o) => {
  chart.value?.setOption(o, { notMerge: true })
})
</script>

<template>
  <div class="metric-chart">
    <div ref="containerRef" class="chart-canvas" />
    <div v-if="loading" class="overlay">loading…</div>
    <div v-if="error" class="overlay error">{{ error }}</div>
    <div
      v-if="!loading && !error && (result?.data?.result?.length ?? 0) === 0"
      class="overlay muted"
    >
      no data in this window
    </div>
  </div>
</template>

<style scoped>
.metric-chart {
  position: relative;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 6px;
  padding: 0.75rem 1rem 1rem;
}
.chart-canvas {
  width: 100%;
  height: 240px;
}
.overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: color-mix(in srgb, var(--card-bg) 80%, transparent);
  color: var(--muted);
  pointer-events: none;
  font-size: 0.85rem;
}
.overlay.error {
  color: var(--status-unreachable);
}
</style>
