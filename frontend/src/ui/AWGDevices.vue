<script setup>
import { onMounted, ref } from 'vue'
import Sheet from './Sheet.vue'
import { t } from '../i18n'
import { apiCall, fmtDateTime, fmtGB, nodeName, readError } from '../store'

const props = defineProps({ user: { type: Object, required: true } })
defineEmits(['close'])
const path = 'api/users/' + encodeURIComponent(props.user.ID) + '/awg'
const data = ref(null)
const invite = ref('')
const label = ref('')
const node = ref('')
const busy = ref(false)
const error = ref('')

async function request(url, options) {
  const response = await apiCall(url, options)
  if (!response.ok) throw new Error(await readError(response))
  return response
}

async function run(action, device) {
  busy.value = true
  error.value = ''
  try {
    if (action === 'create') {
      const response = await request(path, { method: 'POST', body: JSON.stringify({ node: node.value, label: label.value }) })
      invite.value = new URL((await response.json()).path, window.location.origin).href
      label.value = ''
    }
    if (action === 'revoke') {
      await request(path + '/' + encodeURIComponent(device.ID), { method: 'DELETE' })
      invite.value = ''
    }
    data.value = await (await request(path)).json()
    if (!data.value.nodes.some(n => n.IP === node.value)) node.value = data.value.nodes[0]?.IP || ''
  } catch (e) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}

async function copy() {
  try {
    await navigator.clipboard.writeText(invite.value)
  } catch {
    window.prompt(t('awg.copy'), invite.value)
  }
}

onMounted(() => run('read'))
</script>

<template>
  <Sheet :title="'AmneziaWG 3.1 · ' + user.Name" @close="$emit('close')">
    <p v-if="error" role="alert" class="text-sm text-err">{{ error }}</p>
    <p v-if="busy" role="status" class="hint">{{ t('loading') }}</p>
    <template v-if="data">
      <form v-if="data.nodes.length" class="grid gap-2" @submit.prevent="run('create')">
        <label class="label">{{ t('awg.node') }}
          <select v-model="node" class="input mt-1">
            <option v-for="n in data.nodes" :key="n.IP" :value="n.IP">{{ nodeName(n) }}</option>
          </select>
        </label>
        <label class="label">{{ t('awg.device') }}
          <input v-model.trim="label" maxlength="40" required class="input mt-1">
        </label>
        <button type="submit" class="btn btn-primary justify-self-start" :disabled="busy || !label || !node">{{ t('awg.issue') }}</button>
      </form>
      <p v-else class="hint">{{ t('awg.noNodes') }}</p>
      <div v-if="invite" class="grid gap-2">
        <p class="hint">{{ t('awg.invite') }}</p>
        <div class="flex gap-2">
          <input :aria-label="t('awg.invite')" readonly :value="invite" class="input min-w-0">
          <button type="button" class="btn" @click="copy">{{ t('awg.copy') }}</button>
        </div>
      </div>
      <ul v-if="data.devices.length" class="grid gap-2">
        <li v-for="d in data.devices" :key="d.ID" class="flex items-start justify-between gap-2 border-t border-line pt-2">
          <div class="min-w-0 break-words">
            <p>{{ d.Label }} · {{ d.NodeIP }}</p>
            <p class="hint">↑ {{ fmtGB(d.TrafficUp) }} {{ t('gb') }} · ↓ {{ fmtGB(d.TrafficDown) }} {{ t('gb') }}</p>
            <p class="hint">{{ t('awg.handshake') }} · {{ d.Handshake ? fmtDateTime(new Date(d.Handshake * 1000)) : '—' }}</p>
          </div>
          <span v-if="d.Revoked" class="hint">{{ t('awg.revoked') }}</span>
          <button v-else type="button" class="btn text-err" :disabled="busy" @click="run('revoke', d)">{{ t('awg.revoke') }}</button>
        </li>
      </ul>
    </template>
    <template #footer>
      <button type="button" class="btn" :disabled="busy" @click="run('read')">{{ t('awg.reload') }}</button>
    </template>
  </Sheet>
</template>
