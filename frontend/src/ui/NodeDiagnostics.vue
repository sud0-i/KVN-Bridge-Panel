<script setup>
import { onMounted, ref } from 'vue'
import Sheet from './Sheet.vue'
import { t } from '../i18n'
import { apiCall, fmtDateTime, nodeName, readError } from '../store'

const props = defineProps({ node: { type: Object, required: true } })
defineEmits(['close'])

const report = ref(null)
const busy = ref(false)
const error = ref('')
const copied = ref(false)

async function refresh() {
  busy.value = true
  error.value = ''
  copied.value = false
  try {
    const response = await apiCall('api/nodes/' + encodeURIComponent(props.node.IP) + '/diagnostics')
    if (!response.ok) throw new Error(await readError(response))
    report.value = await response.json()
  } catch (e) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}

function journalError(value) {
  return value === 'ssh_key' ? t('diag.noKey') : value
}

async function copyReport() {
  const r = report.value
  const lines = [
    nodeName(props.node) + ' · ' + fmtDateTime(r.at),
    ...r.checks.map(c => (c.ok ? '✓ ' : '× ') + t('diag.' + c.name) + ' · ' + c.result),
    ...[r.config_error, r.update_error, r.warp_error, r.sni_error, journalError(r.journal_error)].filter(Boolean),
    ...r.events.map(e => fmtDateTime(e.at) + ' · ' + e.service + ' · ' + e.message),
  ]
  try {
    await navigator.clipboard.writeText(lines.join('\n'))
    copied.value = true
  } catch {
    window.prompt(t('diag.copy'), lines.join('\n'))
  }
}

onMounted(refresh)
</script>

<template>
  <Sheet :title="t('diag.title') + ' · ' + nodeName(node)" @close="$emit('close')">
    <p v-if="busy" role="status" class="text-sm text-mute">{{ t('loading') }}</p>
    <p v-if="error" role="alert" class="text-sm text-err">{{ error }}</p>
    <template v-if="report">
      <p class="hint">{{ t('diag.hint') }} · {{ fmtDateTime(report.at) }}</p>
      <dl class="grid gap-2 text-sm">
        <div v-for="c in report.checks" :key="c.name" class="flex flex-wrap justify-between gap-x-3 border-b border-line pb-2">
          <dt>{{ t('diag.' + c.name) }}</dt>
          <dd :class="[c.ok ? 'text-ok' : 'text-err', 'break-all']">{{ c.result }}</dd>
        </div>
      </dl>
      <div v-for="field in ['config_error', 'update_error', 'warp_error', 'sni_error']" :key="field">
        <p v-if="report[field]" class="break-words text-sm text-err">{{ t('diag.' + field) }} · {{ report[field] }}</p>
      </div>
      <h3 class="label">{{ t('diag.journal') }}</h3>
      <p v-if="report.journal_error" class="hint">{{ journalError(report.journal_error) }}</p>
      <p v-else-if="!report.events.length" class="hint">{{ t('diag.empty') }}</p>
      <ol v-else class="grid gap-2">
        <li v-for="(e, i) in report.events" :key="i" class="border-b border-line pb-2">
          <div class="hint">{{ fmtDateTime(e.at) }} · {{ e.service }}</div>
          <p class="whitespace-pre-wrap break-words text-sm">{{ e.message }}</p>
        </li>
      </ol>
    </template>
    <template #footer>
      <button v-if="report" type="button" class="btn btn-secondary" @click="copyReport">{{ t(copied ? 'diag.copied' : 'diag.copy') }}</button>
      <button type="button" class="btn btn-primary" :disabled="busy" @click="refresh">{{ t('diag.refresh') }}</button>
    </template>
  </Sheet>
</template>
