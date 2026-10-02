<script setup>
// Небольшой линейный график показателя сервера: одна или две линии на общей шкале,
// без второй оси. Пропуски (агент молчал) — разрывы линии. Подсказка по наведению/касанию.
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { lang } from '../i18n'

const props = defineProps({
  title: { type: String, required: true },
  points: { type: Array, required: true }, // [{t, ...}]
  series: { type: Array, required: true }, // [{key, label, color}]
  since: { type: String, required: true },
  stepMinutes: { type: Number, required: true },
  unit: { type: String, default: '%' },
  max: { type: Number, default: 0 }, // 0 — по данным
  threshold: { type: Number, default: 0 }, // 0 — без порога
})

const box = ref(null)
const width = ref(300)
let ro
onMounted(() => {
  ro = new ResizeObserver(([e]) => { width.value = Math.max(200, Math.floor(e.contentRect.width)) })
  ro.observe(box.value)
})
onBeforeUnmount(() => ro?.disconnect())

const H = 96
const PAD = { l: 36, r: 6, t: 6, b: 18 }
const start = computed(() => new Date(props.since).getTime())
const step = computed(() => props.stepMinutes * 60000)
const slots = computed(() => Math.round((Date.now() - start.value) / step.value) + 1)
const slotW = computed(() => (width.value - PAD.l - PAD.r) / slots.value)
const idx = (p) => Math.round((new Date(p.t).getTime() - start.value) / step.value)
const xc = (i) => PAD.l + (i + 0.5) * slotW.value

const top = computed(() => {
  if (props.max) return props.max
  const m = Math.max(1, ...props.points.flatMap(p => props.series.map(s => p[s.key] ?? 0)))
  const pow = 10 ** Math.floor(Math.log10(m))
  return [1, 2, 2.5, 5, 10].map(k => k * pow).find(n => n >= m)
})
const y = (v) => PAD.t + (H - PAD.t - PAD.b) * (1 - Math.min(v, top.value) / top.value)

const path = (key) => {
  let d = ''
  let prev = null
  for (const p of props.points) {
    const v = p[key]
    const i = idx(p)
    if (v == null) { prev = null; continue }
    d += `${prev !== null && i - prev === 1 ? 'L' : 'M'}${xc(i).toFixed(1)},${y(v).toFixed(1)}`
    prev = i
  }
  return d
}

const fmt = (v) => (v == null ? '—' : (v >= 100 ? Math.round(v) : v) + (props.unit === '%' ? '%' : ' ' + props.unit))

const ticks = computed(() => {
  const every = props.stepMinutes >= 60 ? 24 * 60 : 6 * 60
  const out = []
  const first = Math.ceil(start.value / (every * 60000)) * every * 60000
  for (let ts = first; ts <= Date.now(); ts += every * 60000) {
    const d = new Date(ts)
    out.push({
      x: PAD.l + ((ts - start.value) / step.value) * slotW.value,
      label: props.stepMinutes >= 60
        ? d.toLocaleDateString(lang.value === 'ru' ? 'ru-RU' : 'en-GB', { day: '2-digit', month: '2-digit' })
        : d.toLocaleTimeString(lang.value === 'ru' ? 'ru-RU' : 'en-GB', { hour: '2-digit', minute: '2-digit' }),
    })
  }
  return out
})

const hover = ref(null)
const onMove = (e) => {
  const x = e.clientX - box.value.getBoundingClientRect().left
  const i = Math.round((x - PAD.l) / slotW.value - 0.5)
  let best = null
  for (const p of props.points) if (!best || Math.abs(idx(p) - i) < Math.abs(idx(best) - i)) best = p
  hover.value = best && Math.abs(idx(best) - i) <= 2 ? best : null
}
const hoverX = computed(() => (hover.value ? xc(idx(hover.value)) : 0))
const hoverTime = computed(() => {
  if (!hover.value) return ''
  const d = new Date(hover.value.t)
  const loc = lang.value === 'ru' ? 'ru-RU' : 'en-GB'
  return `${d.toLocaleDateString(loc, { day: '2-digit', month: '2-digit' })} ${d.toLocaleTimeString(loc, { hour: '2-digit', minute: '2-digit' })}`
})
</script>

<template>
  <div class="flex flex-col gap-1">
    <div class="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs">
      <span class="text-mute">{{ title }}</span>
      <!-- Легенда только для двух и более рядов: один ряд называет заголовок -->
      <template v-if="series.length > 1">
        <span v-for="s in series" :key="s.key" class="flex items-center gap-1.5 text-dim">
          <span class="inline-block w-3 h-0.5 rounded" :style="{ background: s.color }"></span>{{ s.label }}
        </span>
      </template>
    </div>
    <div ref="box" class="relative select-none" @pointermove="onMove" @pointerleave="hover = null" @pointerdown="onMove">
      <svg :width="width" :height="H" class="block overflow-visible" role="img" :aria-label="title">
        <g class="text-dim" fill="currentColor" font-size="10">
          <template v-for="v in [0, top]" :key="v">
            <line :x1="PAD.l" :x2="width - PAD.r" :y1="y(v)" :y2="y(v)" stroke="#272D38" stroke-width="1" />
            <text :x="PAD.l - 5" :y="y(v) + 3" text-anchor="end" class="tabular-nums">{{ v }}{{ unit === '%' ? '%' : '' }}</text>
          </template>
          <text v-for="tk in ticks" :key="tk.x" :x="tk.x" :y="H - 4" text-anchor="middle">{{ tk.label }}</text>
        </g>
        <line v-if="threshold" :x1="PAD.l" :x2="width - PAD.r" :y1="y(threshold)" :y2="y(threshold)" stroke="#F0B04A" stroke-opacity="0.5" stroke-width="1" />
        <path v-for="s in series" :key="s.key" :d="path(s.key)" fill="none" :stroke="s.color" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" />
        <g v-if="hover">
          <line :x1="hoverX" :x2="hoverX" :y1="PAD.t" :y2="y(0)" stroke="#7C8596" stroke-width="1" />
          <template v-for="s in series" :key="s.key">
            <circle v-if="hover[s.key] != null" :cx="hoverX" :cy="y(hover[s.key])" r="4" :fill="s.color" stroke="#171B22" stroke-width="2" />
          </template>
        </g>
      </svg>
      <div v-if="hover" class="pointer-events-none absolute top-0 z-10 bg-side border border-line rounded-lg px-2.5 py-1.5 text-xs shadow-xl flex flex-col gap-0.5 whitespace-nowrap"
        :style="hoverX > width / 2 ? { right: (width - hoverX + 10) + 'px' } : { left: (hoverX + 10) + 'px' }">
        <span class="text-mute">{{ hoverTime }}</span>
        <span v-for="s in series" :key="s.key" class="flex items-center gap-1.5">
          <span class="inline-block w-2 h-2 rounded-full" :style="{ background: s.color }"></span>{{ s.label }}: <b class="font-semibold tabular-nums">{{ fmt(hover[s.key]) }}</b>
        </span>
      </div>
    </div>
  </div>
</template>
