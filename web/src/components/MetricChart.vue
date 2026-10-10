<script setup lang="ts">
import { computed } from 'vue'
import { formatChartTime } from '../timeDisplay'
import type { MetricsQueryResponse, MetricsResultValue } from '../api'

const props = defineProps<{
  title: string
  result: MetricsQueryResponse | null
  loading: boolean
  error: string | null
  seriesLabel?: (metric: Record<string, string>) => string
  yUnit?: string
}>()

const width = 720
const height = 240
const plot = { left: 58, right: 702, top: 18, bottom: 204 }
const colours = ['#7c8cff', '#35d6a4', '#f2b95e', '#ff6b82', '#60c9f8', '#b98cff', '#a3e635']

interface Point { x: number; y: number; value: number; timestamp: number }
interface Series { label: string; colour: string; path: string; points: Point[] }

const model = computed(() => {
  const raw = props.result?.data?.result ?? []
  const values = raw.flatMap((series) => series.values)
    .map(([timestamp, value]) => ({ timestamp: timestamp * 1000, value: Number(value) }))
    .filter((point) => Number.isFinite(point.timestamp) && Number.isFinite(point.value))
  if (!values.length) return null

  let minX = Math.min(...values.map((point) => point.timestamp))
  let maxX = Math.max(...values.map((point) => point.timestamp))
  let minY = Math.min(...values.map((point) => point.value))
  let maxY = Math.max(...values.map((point) => point.value))
  if (minX === maxX) { minX -= 1; maxX += 1 }
  if (minY === maxY) {
    const padding = Math.max(Math.abs(minY) * 0.08, 1)
    minY -= padding
    maxY += padding
  } else {
    const padding = (maxY - minY) * 0.08
    minY -= padding
    maxY += padding
  }

  const scaleX = (value: number) => plot.left + ((value - minX) / (maxX - minX)) * (plot.right - plot.left)
  const scaleY = (value: number) => plot.bottom - ((value - minY) / (maxY - minY)) * (plot.bottom - plot.top)
  const labelFor = props.seriesLabel ?? ((metric: Record<string, string>) => JSON.stringify(metric))
  const series: Series[] = raw.map((item: MetricsResultValue, index) => {
    const points = item.values
      .map(([timestamp, value]) => ({ timestamp: timestamp * 1000, value: Number(value) }))
      .filter((point) => Number.isFinite(point.timestamp) && Number.isFinite(point.value))
      .sort((a, b) => a.timestamp - b.timestamp)
      .map((point) => ({ ...point, x: scaleX(point.timestamp), y: scaleY(point.value) }))
    return {
      label: labelFor(item.metric),
      colour: colours[index % colours.length],
      path: points.map((point, pointIndex) => `${pointIndex ? 'L' : 'M'}${point.x.toFixed(2)} ${point.y.toFixed(2)}`).join(' '),
      points,
    }
  }).filter((item) => item.points.length)

  const yTicks = Array.from({ length: 5 }, (_, index) => {
    const ratio = index / 4
    const value = maxY - ratio * (maxY - minY)
    return { value, y: plot.top + ratio * (plot.bottom - plot.top) }
  })
  const xTicks = Array.from({ length: 4 }, (_, index) => {
    const ratio = index / 3
    const value = minX + ratio * (maxX - minX)
    return { value, x: plot.left + ratio * (plot.right - plot.left) }
  })
  return { series, yTicks, xTicks, spanMs: maxX - minX }
})

function formatY(value: number) {
  if (props.yUnit?.trim() === 'B') {
    const units = ['B', 'KB', 'MB', 'GB', 'TB']
    let index = 0
    while (Math.abs(value) >= 1024 && index < units.length - 1) { value /= 1024; index++ }
    return `${value.toFixed(index ? 1 : 0)} ${units[index]}`
  }
  const absolute = Math.abs(value)
  const formatted = absolute >= 1_000_000 ? `${(value / 1_000_000).toFixed(1)}M`
    : absolute >= 1_000 ? `${(value / 1_000).toFixed(1)}k`
      : absolute >= 10 ? value.toFixed(0) : value.toFixed(1)
  return `${formatted}${props.yUnit ?? ''}`
}

function formatTime(timestamp: number, spanMs: number) {
  return formatChartTime(timestamp, spanMs)
}
</script>

<template>
  <section class="metric-chart" :aria-label="title">
    <header>
      <h3>{{ title }}</h3>
      <div v-if="model" class="legend" aria-label="Chart series">
        <span v-for="item in model.series" :key="item.label"><i :style="{ background: item.colour }" />{{ item.label }}</span>
      </div>
    </header>
    <div class="chart-frame">
      <svg v-if="model" :viewBox="`0 0 ${width} ${height}`" role="img" :aria-label="`${title} trend chart`">
        <g class="grid">
          <template v-for="tick in model.yTicks" :key="tick.y">
            <line :x1="plot.left" :x2="plot.right" :y1="tick.y" :y2="tick.y" />
            <text :x="plot.left - 8" :y="tick.y + 4" text-anchor="end">{{ formatY(tick.value) }}</text>
          </template>
          <template v-for="tick in model.xTicks" :key="tick.x">
            <line :x1="tick.x" :x2="tick.x" :y1="plot.top" :y2="plot.bottom" />
            <text :x="tick.x" :y="plot.bottom + 22" :text-anchor="tick.x === plot.left ? 'start' : tick.x === plot.right ? 'end' : 'middle'">{{ formatTime(tick.value, model.spanMs) }}</text>
          </template>
        </g>
        <path v-for="item in model.series" :key="item.label" class="series-line" :d="item.path" :stroke="item.colour" />
      </svg>
      <div v-if="loading" class="overlay">loading…</div>
      <div v-else-if="error" class="overlay error">{{ error }}</div>
      <div v-else-if="!model" class="overlay muted">no data in this window</div>
    </div>
  </section>
</template>

<style scoped>
.metric-chart{min-width:0;padding:.8rem 1rem 1rem;border:1px solid var(--card-border);border-radius:8px;background:var(--card-bg)}
header{display:flex;align-items:flex-start;justify-content:space-between;gap:1rem;min-height:34px}h3{margin:0;color:var(--fg-strong);font-size:.86rem}.legend{display:flex;justify-content:flex-end;flex-wrap:wrap;gap:.35rem .7rem;max-width:70%;color:var(--muted);font-size:.64rem}.legend span{display:flex;align-items:center;gap:.3rem;min-width:0}.legend i{width:14px;height:3px;flex:0 0 auto;border-radius:99px}.chart-frame{position:relative;min-height:240px}svg{display:block;width:100%;height:240px;overflow:visible}.grid line{stroke:rgba(255,255,255,.075);stroke-width:1;vector-effect:non-scaling-stroke}.grid text{fill:var(--muted-2);font-size:10px}.series-line{fill:none;stroke-width:2;stroke-linecap:round;stroke-linejoin:round;vector-effect:non-scaling-stroke}.overlay{position:absolute;inset:0;display:flex;align-items:center;justify-content:center;background:color-mix(in srgb,var(--card-bg) 80%,transparent);color:var(--muted);font-size:.78rem}.overlay.error{color:var(--status-unreachable)}
@media(max-width:720px){.metric-chart{padding:.7rem .6rem .8rem}header{display:block}.legend{justify-content:flex-start;max-width:100%;margin-top:.4rem}.chart-frame{min-height:200px}svg{height:200px}}
</style>
