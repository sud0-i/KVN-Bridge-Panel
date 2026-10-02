<script setup>
import Icon from '../ui/Icon.vue'
import Toggle from '../ui/Toggle.vue'
import Seg from '../ui/Seg.vue'
import { t, lang, setLang } from '../i18n'
import { ref } from 'vue'
import QRCode from 'qrcode'
import { routing, saving, saveMessage, saveError, latestXray, latestXrayError, fetchLatestXray, saveSettings, downloadBackup, logout,
  notify, notifyMessage, notifyError, saveNotify, testNotify, fmtDateTime, twofa, fetchTwofa, twofaPost,
  sshKeys, terminalLog, saveSSHKeys } from '../store'

// SSH-ключи: ключ мастера (только показать) и свои ключи администратора
import { watch } from 'vue'
const keysText = ref('')
const keysMsg = ref('')
const keysErr = ref('')
watch(sshKeys, (k) => { if (k) keysText.value = k.admin.join('\n') }, { immediate: true })
const saveKeys = async () => {
  keysMsg.value = keysErr.value = ''
  try { await saveSSHKeys(keysText.value); keysMsg.value = t('tm.saved') } catch (e) { keysErr.value = e.message }
}
const copyMaster = async () => { try { await navigator.clipboard.writeText(sshKeys.value.master) } catch (e) { /* без HTTPS */ } }

// Двухфакторный вход: QR → подтверждение кодом → резервные коды
const tf = ref({ step: '', qr: '', secret: '', code: '', recovery: [], error: '' })
const tfSetup = async () => {
  tf.value.error = ''
  try {
    const d = await twofaPost('setup')
    tf.value = { ...tf.value, step: 'scan', secret: d.secret, qr: await QRCode.toDataURL(d.uri, { width: 220, margin: 2 }), code: '' }
  } catch (e) { tf.value.error = e.message }
}
const tfEnable = async () => {
  tf.value.error = ''
  try {
    const d = await twofaPost('enable', { code: tf.value.code })
    tf.value = { ...tf.value, step: 'recovery', recovery: d.recovery, code: '' }
    await fetchTwofa()
  } catch (e) { tf.value.error = e.message }
}
const tfDisable = async () => {
  tf.value.error = ''
  try {
    await twofaPost('disable', { code: tf.value.code })
    tf.value = { step: '', qr: '', secret: '', code: '', recovery: [], error: '' }
    await fetchTwofa()
  } catch (e) { tf.value.error = e.message }
}
const tfDone = () => { tf.value = { step: '', qr: '', secret: '', code: '', recovery: [], error: '' } }
const copyRecovery = async () => { try { await navigator.clipboard.writeText(tf.value.recovery.join('\n')) } catch (e) { /* без HTTPS */ } }

const langs = [{ value: 'ru', label: 'Русский' }, { value: 'en', label: 'English' }]
</script>

<template>
  <div class="flex flex-col gap-4 sm:gap-6 max-w-3xl">
    <!-- Двухфакторный вход -->
    <section v-if="twofa" class="card">
      <div class="flex items-start gap-3">
        <Icon name="shield" :class="twofa.enabled ? 'text-ok' : 'text-dim'" />
        <div class="flex flex-col gap-1.5 flex-1">
          <h2 class="h2">{{ t('tf.title') }}</h2>
          <p class="text-[13px] text-mute leading-relaxed">{{ twofa.enabled ? t('tf.on', { n: twofa.recovery_left }) : t('tf.hint') }}</p>
        </div>
      </div>

      <template v-if="tf.step === 'recovery'">
        <p class="text-sm text-warn leading-relaxed">{{ t('tf.saveCodes') }}</p>
        <div class="grid grid-cols-2 gap-2 font-mono text-[15px] bg-surf2 rounded-lg p-4">
          <span v-for="c in tf.recovery" :key="c">{{ c }}</span>
        </div>
        <div class="flex gap-2">
          <button type="button" class="btn" @click="copyRecovery"><Icon name="copy" :size="16" />{{ t('copy') }}</button>
          <button type="button" class="btn btn-primary" @click="tfDone">{{ t('tf.saved') }}</button>
        </div>
      </template>

      <template v-else-if="tf.step === 'scan'">
        <ol class="list-decimal ml-5 text-[13px] text-mute leading-relaxed flex flex-col gap-1">
          <li>{{ t('tf.step1') }}</li>
          <li>{{ t('tf.step2') }}</li>
        </ol>
        <img :src="tf.qr" :alt="t('tf.qr')" class="w-[220px] h-[220px] rounded-lg bg-white self-center sm:self-start">
        <p class="text-xs text-dim">{{ t('tf.manual') }} <span class="font-mono text-mute break-all select-all">{{ tf.secret }}</span></p>
        <div class="flex flex-wrap gap-2 items-center">
          <input v-model.trim="tf.code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="000000"
            :aria-label="t('tf.codeLabel')" class="input w-40 font-mono text-center tracking-[0.3em]" @keyup.enter="tfEnable">
          <button type="button" class="btn btn-primary" :disabled="tf.code.length !== 6" @click="tfEnable">{{ t('tf.enable') }}</button>
          <button type="button" class="btn" @click="tfDone">{{ t('cancel') }}</button>
        </div>
      </template>

      <div v-else-if="twofa.enabled" class="flex flex-wrap gap-2 items-center border-t border-line pt-4">
        <input v-model.trim="tf.code" maxlength="12" :placeholder="t('tf.codeOrRecovery')" :aria-label="t('tf.codeLabel')" class="input w-56 font-mono">
        <button type="button" class="btn btn-danger" :disabled="!tf.code" @click="tfDisable">{{ t('tf.disable') }}</button>
      </div>
      <button v-else type="button" class="btn btn-primary self-start" @click="tfSetup"><Icon name="shield" :size="16" />{{ t('tf.setup') }}</button>
      <p v-if="tf.error" class="text-sm text-err">{{ tf.error }}</p>
      <p class="hint">{{ t('tf.lost') }} <code class="font-mono text-mute">docker compose exec master ./kvn-master reset-2fa</code></p>
    </section>

    <section v-if="sshKeys" class="card">
      <div class="flex flex-col gap-1.5">
        <h2 class="h2">{{ t('tm.keysTitle') }}</h2>
        <p class="text-[13px] text-mute leading-relaxed">{{ t('tm.keysHint') }}</p>
      </div>
      <div>
        <span class="label">{{ t('tm.masterKey') }}</span>
        <div class="flex items-start gap-2">
          <code class="flex-1 min-w-0 font-mono text-xs text-mute break-all bg-surf2 rounded-lg px-3 py-2 select-all">{{ sshKeys.master }}</code>
          <button type="button" class="icon-btn" :aria-label="t('copyLink')" @click="copyMaster"><Icon name="copy" :size="16" /></button>
        </div>
      </div>
      <div>
        <label class="label" for="ssh-admin">{{ t('tm.adminKeys') }}</label>
        <textarea id="ssh-admin" v-model="keysText" rows="3" class="input font-mono text-xs" spellcheck="false" placeholder="ssh-ed25519 AAAA… me@laptop"></textarea>
      </div>
      <div class="flex flex-wrap items-center gap-3">
        <button type="button" class="btn btn-primary" @click="saveKeys">{{ t('tm.save') }}</button>
        <span v-if="keysMsg" class="text-sm text-ok">{{ keysMsg }}</span>
        <span v-if="keysErr" class="text-sm text-err">{{ keysErr }}</span>
      </div>
      <div class="flex flex-col gap-1.5 border-t border-line pt-4">
        <span class="text-sm font-medium">{{ t('tm.log') }}</span>
        <p v-if="!terminalLog.length" class="text-[13px] text-dim">{{ t('tm.logEmpty') }}</p>
        <div v-for="r in terminalLog.slice(0, 10)" :key="r.ID" class="flex flex-wrap gap-x-3 text-[13px]">
          <span class="tabular-nums text-mute">{{ fmtDateTime(r.StartedAt) }}</span>
          <span class="font-mono">{{ r.Node }}</span>
          <span class="text-dim">← {{ r.RemoteIP }}</span>
          <span :class="r.EndedAt ? 'text-dim' : 'text-ok'">{{ r.EndedAt ? r.Reason : t('tm.active') }}</span>
        </div>
      </div>
    </section>

    <section v-if="notify" class="card">
      <div class="flex items-start gap-4">
        <div class="flex flex-col gap-1.5 flex-1">
          <h2 class="h2">{{ t('nt.title') }}</h2>
          <p class="text-[13px] text-mute leading-relaxed">{{ t('nt.hint') }}</p>
        </div>
        <Toggle v-model="notify.form.enabled" :label="t('nt.title')" />
      </div>
      <div class="grid sm:grid-cols-2 gap-4">
        <div>
          <label class="label" for="nt-token">{{ t('nt.token') }}</label>
          <input id="nt-token" v-model="notify.token" type="password" autocomplete="off" class="input font-mono"
            :placeholder="notify.hasToken ? t('nt.tokenSet') : '123456789:AA…'">
          <p class="hint mt-1.5">{{ t('nt.tokenHint') }}
            <button v-if="notify.hasToken" type="button" class="text-err hover:underline" @click="saveNotify(true)">{{ t('nt.clearToken') }}</button></p>
        </div>
        <div>
          <label class="label" for="nt-chats">{{ t('nt.chats') }}</label>
          <input id="nt-chats" v-model="notify.form.chats" type="text" class="input font-mono" placeholder="123456789" inputmode="numeric">
          <p class="hint mt-1.5">{{ t('nt.chatsHint') }}</p>
        </div>
      </div>
      <fieldset class="flex flex-col gap-3 border-t border-line pt-4">
        <legend class="sr-only">{{ t('nt.what') }}</legend>
        <div v-for="k in ['nodes', 'updates', 'users']" :key="k" class="flex items-start gap-4">
          <div class="flex flex-col gap-1 flex-1">
            <span class="text-sm font-medium">{{ t('nt.' + k) }}</span>
            <span class="text-[13px] text-mute leading-relaxed">{{ t('nt.' + k + 'Hint') }}</span>
          </div>
          <Toggle v-model="notify.form[k]" :label="t('nt.' + k)" />
        </div>
      </fieldset>
      <div class="border-t border-line pt-4 flex flex-col gap-1.5">
        <span class="text-[13px] text-mute">{{ t('nt.lang') }}</span>
        <Seg v-model="notify.form.lang" :options="langs" :label="t('nt.lang')" class="self-start" />
      </div>
      <div v-if="notify.status.last" class="border-t border-line pt-4 text-[13px] flex flex-col gap-1">
        <span class="text-mute">{{ t('nt.last') }}</span>
        <span v-if="notify.status.last.sent_at" class="text-ok">{{ t('nt.sent', { at: fmtDateTime(notify.status.last.sent_at), via: notify.status.last.via === 'master' ? t('nt.viaMaster') : notify.status.last.via }) }}</span>
        <span v-else-if="notify.status.last.failed" class="text-err">{{ t('nt.failed') }}: {{ notify.status.last.error }}</span>
        <span v-else class="text-warn">{{ t('nt.pending') }}<template v-if="notify.status.last.error"> · {{ notify.status.last.error }}</template></span>
      </div>
      <div class="flex flex-wrap items-center gap-3">
        <button type="button" class="btn btn-primary" @click="saveNotify()">{{ t('save') }}</button>
        <button type="button" class="btn" :disabled="!notify.form.enabled" @click="testNotify">{{ t('nt.test') }}</button>
        <span v-if="notifyMessage" class="text-ok text-sm">{{ notifyMessage }}</span>
        <span v-if="notifyError" class="text-err text-sm">{{ notifyError }}</span>
      </div>
    </section>

    <section v-if="routing" class="card">
      <h2 class="h2">{{ t('updates') }}</h2>
      <div>
        <label class="label" for="xray-ver">{{ t('xrayVersion') }}</label>
        <div class="flex flex-wrap items-center gap-2">
          <input id="xray-ver" v-model.trim="routing.xray_version" placeholder="26.3.27" class="input w-40 font-mono">
          <button type="button" class="btn" @click="fetchLatestXray">{{ t('checkLatest') }}</button>
          <button v-if="latestXray && latestXray !== routing.xray_version" type="button" class="btn font-mono" @click="routing.xray_version = latestXray">
            {{ t('useVersion', { v: latestXray }) }}</button>
          <span v-else-if="latestXray" class="text-sm text-ok flex items-center gap-1"><Icon name="check" :size="16" />{{ t('isLatest') }}</span>
        </div>
        <p v-if="latestXrayError" class="text-sm text-err mt-1.5">{{ latestXrayError }}</p>
        <p class="hint mt-1.5">{{ t('xrayVersionHint') }}</p>
      </div>
      <div class="border-t border-line pt-4 flex items-start gap-4">
        <div class="flex flex-col gap-1.5 flex-1">
          <span class="text-sm font-medium">{{ t('geoUpdate') }}</span>
          <span class="text-[13px] text-mute leading-relaxed">{{ t('geoUpdateHint') }}</span>
        </div>
        <Toggle v-model="routing.geo_update" :label="t('geoUpdate')" />
      </div>
      <p class="hint border-t border-line pt-4">{{ t('agentUpdateNote') }}</p>
      <div class="flex flex-wrap items-center gap-3">
        <button type="button" class="btn btn-primary" :disabled="saving" @click="saveSettings">{{ t('save') }}</button>
        <span v-if="saveMessage" class="text-ok text-sm">{{ saveMessage }}</span>
        <span v-if="saveError" class="text-err text-sm">{{ saveError }}</span>
      </div>
    </section>

    <section class="card">
      <div class="flex flex-col gap-1.5">
        <h2 class="h2">{{ t('backup') }}</h2>
        <p class="text-[13px] text-mute leading-relaxed">{{ t('backupHint') }}</p>
      </div>
      <button type="button" class="btn self-start" @click="downloadBackup"><Icon name="download" :size="16" />{{ t('st.download') }}</button>
    </section>

    <section class="card">
      <h2 class="h2">{{ t('st.language') }}</h2>
      <Seg :model-value="lang" @update:model-value="setLang" :options="langs" :label="t('st.language')" class="self-start" />
    </section>

    <button type="button" class="btn btn-danger self-start" @click="logout"><Icon name="logout" :size="16" />{{ t('logout') }}</button>
  </div>
</template>
