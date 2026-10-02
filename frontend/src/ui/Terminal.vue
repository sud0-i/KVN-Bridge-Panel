<script setup>
// Терминал ноды: код 2FA → одноразовый пропуск → WebSocket → SSH с мастера.
// Копирование — выделением; вставка — Ctrl+V / Ctrl+Shift+V / правая кнопка.
// На телефоне — панель клавиш (Esc, Tab, Ctrl, стрелки) и строка ввода с кнопкой «Отправить».
import { ref, computed, nextTick, onMounted, onBeforeUnmount } from 'vue'
import Icon from './Icon.vue'
import { t } from '../i18n'
import { twofa, terminalTicket, nodeName } from '../store'

const props = defineProps({ node: { type: Object, required: true } })
const emit = defineEmits(['close', 'go'])

const state = ref('code') // code → connecting → open → closed
const code = ref('')
const error = ref('')
const reason = ref('')
const idleMin = ref(15)
const toast = ref('')
const line = ref('')
const ctrl = ref(false)
const touch = window.matchMedia?.('(pointer: coarse)').matches ?? false

const box = ref(null)
const codeInput = ref(null)
let term = null
let fit = null
let ws = null
let ro = null
let toastTimer = null

const enc = new TextEncoder()
const sendRaw = (s) => { if (ws?.readyState === WebSocket.OPEN) ws.send(enc.encode(s)) }
const flash = (msg) => { toast.value = msg; clearTimeout(toastTimer); toastTimer = setTimeout(() => (toast.value = ''), 1600) }

// Буфер обмена: Clipboard API есть только по HTTPS — иначе запасной путь через execCommand
const copyText = async (text) => {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
  } catch (e) {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    try { document.execCommand('copy') } catch (e2) { /* нечего делать */ }
    ta.remove()
  }
  flash(t('tm.copied'))
}
const pasteClipboard = async () => {
  try {
    const text = await navigator.clipboard.readText()
    if (text) term?.paste(text)
  } catch (e) {
    flash(t('tm.pasteFail'))
  }
  term?.focus()
}
const copySelection = () => copyText(term?.getSelection())

const connect = async () => {
  error.value = ''
  state.value = 'connecting'
  let ticket
  try {
    ticket = await terminalTicket(props.node, code.value)
  } catch (e) {
    error.value = e.message
    state.value = 'code'
    code.value = ''
    nextTick(() => codeInput.value?.focus())
    return
  }
  code.value = ''
  state.value = 'open'
  await nextTick()
  await startTerm()
  const u = new URL('api/terminal', window.location.href)
  u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:'
  u.hash = ''
  u.search = new URLSearchParams({ ticket, cols: term.cols, rows: term.rows })
  ws = new WebSocket(u)
  ws.binaryType = 'arraybuffer'
  ws.onmessage = (ev) => {
    if (typeof ev.data !== 'string') {
      term.write(new Uint8Array(ev.data))
      return
    }
    const m = JSON.parse(ev.data)
    if (m.type === 'ready') { idleMin.value = m.idle_minutes; term.focus() }
    if (m.type === 'error') { term.write(`\r\n\x1b[31m${m.message}\x1b[0m\r\n`); reason.value = m.message }
    if (m.type === 'closed') reason.value = m.reason
  }
  ws.onclose = () => { state.value = 'closed'; ws = null }
}

const startTerm = async () => {
  if (term) { term.reset(); return }
  const [{ Terminal }, { FitAddon }] = await Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit'), import('@xterm/xterm/css/xterm.css')])
  term = new Terminal({
    fontFamily: '"IBM Plex Mono", ui-monospace, monospace',
    fontSize: touch ? 13 : 14,
    cursorBlink: true,
    scrollback: 5000,
    theme: { background: '#0E1014', foreground: '#E7EAF0', cursor: '#6AA8FF', selectionBackground: '#1A2A44' },
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(box.value)
  fit.fit()
  term.onData((d) => {
    // Залипший Ctrl с панели клавиш: следующая буква уходит как Ctrl+буква
    if (ctrl.value && d.length === 1 && /[a-z@[\\\]^_]/i.test(d)) {
      d = String.fromCharCode(d.toUpperCase().charCodeAt(0) & 31)
      ctrl.value = false
    }
    sendRaw(d)
  })
  term.onSelectionChange(() => { const s = term.getSelection(); if (s) copyText(s) })
  term.onResize(({ cols, rows }) => { if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'resize', cols, rows })) })
  term.attachCustomKeyEventHandler((e) => {
    if (e.type !== 'keydown' || !e.ctrlKey) return true
    const k = e.key.toLowerCase()
    // Ctrl+V и Ctrl+Shift+V — вставку делает сам браузер (событие paste)
    if (k === 'v') return false
    if (e.shiftKey && k === 'c') { e.preventDefault(); copySelection(); return false }
    return true
  })
  box.value.addEventListener('contextmenu', (e) => {
    e.preventDefault()
    if (term.hasSelection()) copySelection()
    else pasteClipboard()
  })
  ro = new ResizeObserver(() => { try { fit.fit() } catch (e) { /* скрыт */ } })
  ro.observe(box.value)
}

// Панель клавиш для телефона
const keys = [
  { label: 'Esc', seq: '\x1b' }, { label: 'Tab', seq: '\t' }, { label: 'Ctrl', ctrl: true },
  { label: '^C', seq: '\x03' }, { label: '^D', seq: '\x04' },
  { label: '←', seq: '\x1b[D' }, { label: '↑', seq: '\x1b[A' }, { label: '↓', seq: '\x1b[B' }, { label: '→', seq: '\x1b[C' },
  { label: '|', seq: '|' }, { label: '/', seq: '/' }, { label: '-', seq: '-' }, { label: '~', seq: '~' },
]
const press = (k) => {
  if (k.ctrl) { ctrl.value = !ctrl.value; return }
  sendRaw(k.seq)
}
const sendLine = () => {
  sendRaw(line.value.replace(/\r?\n/g, '\r') + '\r')
  line.value = ''
}

const close = () => { ws?.close(); emit('close') }
const onKey = (e) => { if (e.key === 'Escape' && state.value !== 'open') close() }
const needs2fa = computed(() => twofa.value && !twofa.value.enabled)

onMounted(() => {
  window.addEventListener('keydown', onKey)
  document.body.style.overflow = 'hidden'
  nextTick(() => codeInput.value?.focus())
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKey)
  document.body.style.overflow = ''
  ws?.close()
  ro?.disconnect()
  term?.dispose()
  clearTimeout(toastTimer)
})
</script>

<template>
  <div class="fixed inset-0 z-50 bg-ground flex flex-col h-[100dvh]" role="dialog" aria-modal="true" :aria-label="t('tm.title', { name: nodeName(node) })">
    <header class="flex items-center gap-3 px-4 py-2.5 border-b border-line bg-side">
      <span class="font-medium truncate">{{ t('tm.title', { name: nodeName(node) }) }}</span>
      <span class="font-mono text-xs text-dim hidden sm:inline">root@{{ node.IP }}</span>
      <span class="flex-1"></span>
      <span v-if="state === 'open' && !touch" class="text-xs text-dim hidden md:inline">{{ t('tm.keysHelp') }}</span>
      <button type="button" class="icon-btn" :aria-label="t('tm.close')" :title="t('tm.close')" @click="close"><Icon name="close" :size="18" /></button>
    </header>

    <!-- Код 2FA -->
    <div v-if="state === 'code' || state === 'connecting'" class="flex-1 flex items-start sm:items-center justify-center p-4">
      <div class="card w-full max-w-sm">
        <template v-if="needs2fa">
          <p class="text-sm text-mute leading-relaxed">{{ t('tm.need2fa') }}</p>
          <button type="button" class="btn btn-primary self-start" @click="emit('go', 'settings'); close()"><Icon name="shield" :size="16" />{{ t('tm.go2fa') }}</button>
        </template>
        <form v-else class="flex flex-col gap-3" @submit.prevent="connect">
          <p class="text-sm text-mute leading-relaxed">{{ t('tm.codeHint') }}</p>
          <label class="label" for="tm-code">{{ t('tm.codeLabel') }}</label>
          <input id="tm-code" ref="codeInput" v-model.trim="code" inputmode="numeric" autocomplete="one-time-code" maxlength="12" placeholder="000000"
            class="input font-mono text-center tracking-[0.3em]" :disabled="state === 'connecting'"
            @input="code.length === 6 && /^\d+$/.test(code) && connect()">
          <p v-if="error" class="text-sm text-err" role="alert">{{ error }}</p>
          <button type="submit" class="btn btn-primary" :disabled="!code || state === 'connecting'">{{ state === 'connecting' ? t('tm.connecting') : t('tm.connect') }}</button>
        </form>
      </div>
    </div>

    <!-- Терминал -->
    <div v-show="state === 'open' || state === 'closed'" class="flex-1 min-h-0 relative">
      <div ref="box" class="absolute inset-0 p-2"></div>
      <div v-if="state === 'closed'" class="absolute inset-x-0 bottom-4 flex justify-center px-4">
        <div class="bg-side border border-line rounded-xl px-4 py-3 flex flex-wrap items-center gap-3 shadow-xl">
          <span class="text-sm text-mute">{{ t('tm.closed') }}<template v-if="reason">: {{ reason }}</template></span>
          <button type="button" class="btn btn-primary" @click="state = 'code'; reason = ''; nextTick(() => codeInput?.focus())">{{ t('tm.reconnect') }}</button>
        </div>
      </div>
      <div v-if="toast" class="absolute top-3 right-3 bg-side border border-line rounded-lg px-3 py-1.5 text-xs text-mute shadow-xl" role="status">{{ toast }}</div>
    </div>

    <!-- Телефон: клавиши и строка ввода; компьютер: кнопки копирования и вставки -->
    <footer v-if="state === 'open'" class="border-t border-line bg-side px-2 py-2 flex flex-col gap-2" style="padding-bottom: max(0.5rem, env(safe-area-inset-bottom))">
      <div class="flex gap-1.5 overflow-x-auto">
        <template v-if="touch">
          <button type="button" class="btn shrink-0 h-9" @click="pasteClipboard">{{ t('tm.paste') }}</button>
          <button v-for="k in keys" :key="k.label" type="button" @click="press(k)"
            :class="['shrink-0 min-w-[44px] h-9 px-2.5 rounded-md border font-mono text-sm', k.ctrl && ctrl ? 'bg-acc text-[#0B1220] border-acc' : 'bg-surf2 border-line text-fg']">{{ k.label }}</button>
          <button type="button" class="btn shrink-0 h-9" @click="copySelection"><Icon name="copy" :size="15" />{{ t('tm.copy') }}</button>
        </template>
        <template v-else>
          <span class="flex-1"></span>
          <button type="button" class="btn shrink-0 h-9" @click="copySelection"><Icon name="copy" :size="15" />{{ t('tm.copy') }}</button>
          <button type="button" class="btn shrink-0 h-9" @click="pasteClipboard">{{ t('tm.paste') }}</button>
        </template>
      </div>
      <form v-if="touch" class="flex gap-2" @submit.prevent="sendLine">
        <input v-model="line" type="text" class="input flex-1 font-mono" :placeholder="t('tm.line')" :aria-label="t('tm.line')"
          autocapitalize="off" autocorrect="off" autocomplete="off" spellcheck="false" enterkeyhint="send">
        <button type="submit" class="btn btn-primary">{{ t('tm.send') }}</button>
      </form>
      <p v-else class="text-[11px] text-dim px-1">{{ t('tm.idle', { n: idleMin }) }}</p>
    </footer>
  </div>
</template>
