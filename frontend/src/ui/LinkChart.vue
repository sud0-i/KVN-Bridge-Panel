<script setup>
// Качество канала: задержка (линия) и потери (столбики) — два графика с общей
// шкалой времени, без второй оси. Пропуски в данных (мост молчал) — разрывы линии.
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { t, lang } from '../i18n'

const props = defineProps({
  points: { type: Array, required: true }, // [{t, loss_pct, avg_ms, max_ms, sent}]
  since: { type: String, required: true },
  stepMinutes: { type: Number, required: true },
  alertPct: { type: Number, default: 10 },
})

const box = ref(null)
const width = ref(600)
let ro
onMounted(() => {
  ro = new ResizeObserver(([e]) => { width.value = Math.max(260, Math.floor(e.contentRect.width)) })
  ro.observe(box.value)
})
onBeforeUnmount(() => ro?.disconnect())

const PAD = { l: 40, r: 8 }
const H_LAT = 120
const H_LOSS = 72
const start = computed(() => new Date(props.since).getTime())
const step = computed(() => props.stepMinutes * 60000)
const slots = computed(() => Math.round((Date.now() - start.value) / step.value) + 1)
const plotW = computed(() => width.value - PAD.l - PAD.r)
const slotW = computed(() => plotW.value / slots.value)
const idx = (p) => Math.round((new Date(p.t).getTime() - start.value) / step.value)
const xc = (i) => PAD.l + (i + 0.5) * slotW.value

// «Круглый» верх шкалы
const niceMax = (v, min) => {
  const m = Math.max(v, min)
  const pow = 10 ** Math.floor(Math.log10(m))
  return [1, 2, 2.5, 5, 10].map(k => k * pow).find(n => n >= m)
}
const latMax = computed(() => niceMax(Math.max(0, ...props.points.map(p => p.avg_ms)), 20))
const lossMax = computed(() => niceMax(Math.max(0, ...props.points.map(p => p.loss_pct)), props.alertPct * 2))
const yLat = (v) => 8 + (H_LAT - 28) * (1 - v / latMax.value)
const yLoss = (v) => 8 + (H_LOSS - 16) * (1 - v / lossMax.value)

// Линия задержки: разрыв, если соседние точки не рядом или ответов не было
const latPath = computed(() => {
  let d = ''
  let prev = null
  for (const p of props.points) {
    const i = idx(p)
    if (p.avg_ms <= 0) { prev = null; continue }
    d += `${prev !== null && i - prev === 1 ? 'L' : 'M'}${xc(i).toFixed(1)},${yLat(p.avg_ms).toFixed(1)}`
    prev = i
  }
  return d
})
const latArea = computed(() => {
  // Заливка под каждым непрерывным куском линии
  const parts = []
  let run = []
  let prev = null
  for (const p of props.points) {
    const i = idx(p)
    if (p.avg_ms <= 0 || (prev !== null && i - prev !== 1)) { if (run.length) parts.push(run); run = [] }
    if (p.avg_ms > 0) run.push([xc(i), yLat(p.avg_ms)])
    prev = p.avg_ms > 0 ? i : null
  }
  if (run.length) parts.push(run)
  const base = yLat(0)
  return parts.filter(r => r.length > 1).map(r =>
    `M${r[0][0]},${base}` + r.map(([x, y]) => `L${x.toFixed(1)},${y.toFixed(1)}`).join('') + `L${r[r.length - 1][0]},${base}Z`).join('')
})

// Столбики потерь: ширина не больше 24 px, зазор 2 px между соседними
const barW = computed(() => Math.max(1, Math.min(24, slotW.value - 2)))
const bars = computed(() => props.points.filter(p => p.loss_pct > 0).map(p => {
  const h = Math.max(2, yLoss(0) - yLoss(p.loss_pct))
  return { x: xc(idx(p)) - barW.value / 2, y: yLoss(0) - h, h, over: p.loss_pct >= props.alertPct }
}))
// Скругление 4 px только у вершины столбика
const barPath = (b) => {
  const w = barW.value
  const r = Math.min(4, w / 2, b.h)
  const { x, y, h } = b
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`
}

// Подписи времени: каждые 4 часа за сутки, каждые сутки за неделю
const locale = () => (lang.value === 'ru' ? 'ru-RU' : 'en-GB')
const ticks = computed(() => {
  const every = props.stepMinutes >= 60 ? 24 * 60 : 4 * 60
  const out = []
  const first = Math.ceil(start.value / (every * 60000)) * every * 60000
  for (let ts = first; ts <= Date.now(); ts += every * 60000) {
    const i = (ts - start.value) / step.value
    const d = new Date(ts)
    out.push({
      x: PAD.l + i * slotW.value,
      label: props.stepMinutes >= 60
        ? d.toLocaleDateString(locale(), { day: '2-digit', month: '2-digit' })
        : d.toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit' }),
    })
  }
  return out
})

// Подсказка: ближайшая точка к курсору (или пальцу)
const hover = ref(null)
const onMove = (e) => {
  const rect = box.value.getBoundingClientRect()
  const x = e.clientX - rect.left
  const i = Math.round((x - PAD.l) / slotW.value - 0.5)
  let best = null
  for (const p of props.points) {
    if (!best || Math.abs(idx(p) - i) < Math.abs(idx(best) - i)) best = p
  }
  hover.value = best && Math.abs(idx(best) - i) <= 2 ? best : null
}
const hoverX = computed(() => (hover.value ? xc(idx(hover.value)) : 0))
const fmtTime = (iso) => {
  const d = new Date(iso)
  const end = new Date(d.getTime() + step.value)
  const hm = (x) => x.toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit' })
  return `${d.toLocaleDateString(locale(), { day: '2-digit', month: '2-digit' })} ${hm(d)}–${hm(end)}`
}
</script>

<template>
  <div ref="box" class="relative select-none" @pointermove="onMove" @pointerleave="hover = null" @pointerdown="onMove">
    <!-- Задержка -->
    <div class="text-xs text-mute mb-1">{{ t('ln.latency') }}</div>
    <svg :width="width" :height="H_LAT" class="block overflow-visible" role="img" :aria-label="t('ln.latency')">
      <g class="text-dim" fill="currentColor" font-size="11">
        <template v-for="v in [0, latMax / 2, latMax]" :key="'l' + v">
          <line :x1="PAD.l" :x2="width - PAD.r" :y1="yLat(v)" :y2="yLat(v)" stroke="#272D38" stroke-width="1" />
          <text :x="PAD.l - 6" :y="yLat(v) + 4" text-anchor="end" class="tabular-nums">{{ v }}</text>
        </template>
      </g>
      <path :d="latArea" fill="#6AA8FF" fill-opacity="0.1" />
      <path :d="latPath" fill="none" stroke="#6AA8FF" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" />
      <g v-if="hover && hover.avg_ms > 0">
        <line :x1="hoverX" :x2="hoverX" :y1="4" :y2="yLat(0)" stroke="#7C8596" stroke-width="1" />
        <circle :cx="hoverX" :cy="yLat(hover.avg_ms)" r="4" fill="#6AA8FF" stroke="#171B22" stroke-width="2" />
      </g>
      <g class="text-dim" fill="currentColor" font-size="11">
        <text v-for="tk in ticks" :key="tk.x" :x="tk.x" :y="H_LAT - 4" text-anchor="middle">{{ tk.label }}</text>
      </g>
    </svg>

    <!-- Потери -->
    <div class="text-xs text-mute mt-3 mb-1 flex flex-wrap items-center gap-x-3">
      <span>{{ t('ln.loss') }}</span>
      <span class="flex items-center gap-1.5 text-dim"><span class="inline-block w-4 h-px bg-warn/60"></span>{{ t('ln.threshold', { n: alertPct }) }}</span>
    </div>
    <svg :width="width" :height="H_LOSS" class="block overflow-visible" role="img" :aria-label="t('ln.loss')">
      <g class="text-dim" fill="currentColor" font-size="11">
        <template v-for="v in [0, lossMax]" :key="'s' + v">
          <line :x1="PAD.l" :x2="width - PAD.r" :y1="yLoss(v)" :y2="yLoss(v)" stroke="#272D38" stroke-width="1" />
          <text :x="PAD.l - 6" :y="yLoss(v) + 4" text-anchor="end" class="tabular-nums">{{ v }}%</text>
        </template>
      </g>
      <!-- Порог уведомления -->
      <line :x1="PAD.l" :x2="width - PAD.r" :y1="yLoss(alertPct)" :y2="yLoss(alertPct)" stroke="#F0B04A" stroke-opacity="0.6" stroke-width="1" />
      <path v-for="(b, i) in bars" :key="i" :d="barPath(b)" :fill="b.over ? '#F07A6E' : '#6AA8FF'" />
      <line v-if="hover" :x1="hoverX" :x2="hoverX" :y1="4" :y2="yLoss(0)" stroke="#7C8596" stroke-width="1" />
    </svg>

    <!-- Подсказка -->
    <div v-if="hover" class="pointer-events-none absolute top-6 z-10 bg-side border border-line rounded-lg px-3 py-2 text-xs shadow-xl flex flex-col gap-0.5 whitespace-nowrap"
      :style="hoverX > width / 2 ? { right: (width - hoverX + 10) + 'px' } : { left: (hoverX + 10) + 'px' }">
      <span class="text-mute">{{ fmtTime(hover.t) }}</span>
      <span>{{ t('ln.latencyShort') }}: <b class="font-semibold">{{ hover.avg_ms > 0 ? hover.avg_ms + ' ' + t('ln.ms') : '—' }}</b>
        <span v-if="hover.max_ms" class="text-dim"> ({{ t('ln.max') }} {{ hover.max_ms }})</span></span>
      <span>{{ t('ln.loss') }}: <b :class="['font-semibold', hover.loss_pct >= alertPct ? 'text-err' : '']">{{ hover.loss_pct }}%</b>
        <span class="text-dim"> · {{ hover.sent }} {{ t('ln.probes') }}</span></span>
    </div>
  </div>
</template>
