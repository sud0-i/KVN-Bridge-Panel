// Состояние панели и запросы к API. Экраны читают и меняют его напрямую.
import { ref, computed } from 'vue'
import QRCode from 'qrcode'
import { t, lang } from './i18n'

// ================= АВТОРИЗАЦИЯ =================
export const token = ref(localStorage.getItem('token') || '')

export const logout = () => {
  token.value = ''
  localStorage.removeItem('token')
}

// Запрос к API с токеном; 401 — разлогиниваем
export const apiCall = async (url, options = {}) => {
  const headers = {
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${token.value}`,
    ...options.headers
  }
  const res = await fetch(url, { ...options, headers })
  if (res.status === 401) {
    logout()
    throw new Error('Session expired')
  }
  return res
}

export const readError = async (res) => {
  try { return (await res.json()).error || res.statusText } catch (e) { return res.statusText }
}

// Вход. С двухфакторным входом сервер сначала отвечает need_code — тогда спрашиваем код.
export class NeedCode extends Error {}
export const login = async (password, code = '') => {
  const res = await fetch('api/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password, code })
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    if (data.need_code) throw new NeedCode(code ? data.error : '')
    if (res.status === 429) throw new Error(t('tooManyAttempts'))
    throw new Error(t('wrongPassword'))
  }
  token.value = data.token
  localStorage.setItem('token', data.token)
  loadAll()
}

// Свежая копия базы — файлом на компьютер администратора
export const downloadBackup = async () => {
  try {
    const res = await apiCall('api/backup')
    if (!res.ok) {
      alert(await readError(res))
      return
    }
    const name = (res.headers.get('Content-Disposition') || '').match(/filename="?([^"]+)"?/)?.[1] || 'kvn-backup.sqlite'
    const url = URL.createObjectURL(await res.blob())
    const a = document.createElement('a')
    a.href = url
    a.download = name
    a.click()
    URL.revokeObjectURL(url)
  } catch (error) {
    console.error(error)
  }
}

// ================= ФОРМАТИРОВАНИЕ =================
export const GB = 1073741824
export const used = (u) => u.TrafficUp + u.TrafficDown
export const fmtGB = (bytes) => {
  const gb = bytes / GB
  return gb >= 100 ? gb.toFixed(0) : gb >= 10 ? gb.toFixed(1) : gb.toFixed(2)
}
const locale = () => (lang.value === 'ru' ? 'ru-RU' : 'en-GB')
export const fmtDate = (iso) => new Date(iso).toLocaleDateString(locale())
export const fmtDateTime = (iso) => new Date(iso).toLocaleString(locale(), { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })
export const isExpired = (u) => u.ExpiresAt && new Date(u.ExpiresAt) < new Date()
// Доля квоты, 0–100; без квоты — null
export const quotaPct = (u) => (u.TrafficQuota ? Math.min(100, Math.round((used(u) / u.TrafficQuota) * 100)) : null)
export const subLink = (u) => `${window.location.origin}/sub/${u.SubToken || u.ID}`

// «12 с назад», «5 мин назад»…
export const ago = (iso) => {
  if (!iso || iso.startsWith('0001')) return t('never')
  const s = Math.max(0, Math.round((Date.now() - new Date(iso)) / 1000))
  if (s < 60) return t('agoSec', { n: s })
  if (s < 3600) return t('agoMin', { n: Math.round(s / 60) })
  if (s < 86400) return t('agoHour', { n: Math.round(s / 3600) })
  return t('agoDay', { n: Math.round(s / 86400) })
}

// ================= ПОЛЬЗОВАТЕЛИ =================
export const users = ref([])
export const loadingUsers = ref(false)

export const fetchUsers = async () => {
  if (!token.value) return
  loadingUsers.value = true
  try {
    const res = await apiCall('api/users')
    users.value = await res.json()
  } catch (error) {
    console.error(error)
  } finally {
    loadingUsers.value = false
  }
}

// Окно пользователя: null — закрыто, {} с editing=null — создание
export const userModal = ref(null)
const dateInput = (iso) => (iso ? iso.slice(0, 10) : '')

export const openUserModal = (user = null) => {
  userModal.value = {
    editing: user,
    form: user
      ? { name: user.Name, ipLimit: user.IPLimit, quotaGB: user.TrafficQuota ? +(user.TrafficQuota / GB).toFixed(2) : 0, expires: dateInput(user.ExpiresAt) }
      : { name: '', ipLimit: 5, quotaGB: 0, expires: '' }
  }
}

export const saveUser = async () => {
  const { editing, form: f } = userModal.value
  if (!f.name) return
  const body = {
    name: f.name,
    ip_limit: Number(f.ipLimit) || 0,
    traffic_quota: Math.round((Number(f.quotaGB) || 0) * GB),
    // Срок — до конца выбранного дня по UTC, как при импорте
    expires_at: f.expires ? `${f.expires}T23:59:59Z` : null
  }
  const res = editing
    ? await apiCall(`api/users/${editing.ID}`, { method: 'PATCH', body: JSON.stringify(body) })
    : await apiCall('api/users', { method: 'POST', body: JSON.stringify(body) })
  if (!res.ok) {
    alert(await readError(res))
    return
  }
  userModal.value = null
  await fetchUsers()
}

// Действия из окна правки: сброс трафика, новая ссылка, новый ключ
export const userAction = async (path, body, confirmKey) => {
  if (confirmKey && !confirm(t(confirmKey))) return
  const res = await apiCall(`api/users/${userModal.value.editing.ID}/${path}`, { method: 'POST', body: body ? JSON.stringify(body) : undefined })
  if (!res.ok) {
    alert(await readError(res))
    return
  }
  userModal.value.editing = await res.json()
  await fetchUsers()
  alert(t('done'))
}

export const importUsers = async (text) => {
  const res = await apiCall('api/users/import', { method: 'POST', body: JSON.stringify({ text }) })
  const data = await res.json()
  if (!res.ok) return { error: data.error, problems: data.problems || [] }
  alert(t('imported', { n: data.created }))
  await fetchUsers()
  return null
}

export const toggleUserStatus = async (user) => {
  const status = user.Status === 'active' ? 'blocked' : 'active'
  try {
    await apiCall(`api/users/${user.ID}/status`, { method: 'PATCH', body: JSON.stringify({ status }) })
    await fetchUsers()
  } catch (error) {
    console.error(error)
  }
}

export const deleteUser = async (user) => {
  if (!confirm(t('confirmDeleteUser'))) return
  try {
    await apiCall(`api/users/${user.ID}`, { method: 'DELETE' })
    await fetchUsers()
  } catch (error) {
    console.error(error)
  }
}

// Окно «поделиться»: QR и ссылка
export const shareUser = ref(null)
export const shareQR = ref('')
export const openShare = async (user) => {
  shareUser.value = user
  shareQR.value = ''
  shareQR.value = await QRCode.toDataURL(subLink(user), { width: 256, margin: 2 })
}

export const copySubLink = async (user) => {
  try {
    await navigator.clipboard.writeText(subLink(user))
    alert(t('linkCopied') + subLink(user))
  } catch (e) {
    prompt(t('copyLink'), subLink(user)) // без HTTPS буфер обмена недоступен
  }
}

// ================= НОДЫ =================
export const nodes = ref([])
export const loadingNodes = ref(false)

export const fetchNodes = async () => {
  if (!token.value) return
  loadingNodes.value = true
  try {
    const res = await apiCall('api/nodes')
    nodes.value = await res.json()
  } catch (error) {
    console.error(error)
  } finally {
    loadingNodes.value = false
  }
}

// Окно установки ноды: null — закрыто; redeployIP — «переустановить эту ноду»
export const nodeModal = ref(null)
export const openDeploy = () => { nodeModal.value = { redeployIP: '', ip: '', type: 'bridge', sni: '', password: '', message: '' } }
export const openRedeploy = (ip) => {
  const n = nodes.value.find(x => x.IP === ip)
  nodeModal.value = { redeployIP: ip, ip: '', type: 'bridge', sni: '', password: '', message: '', byKey: !!n && hasPanelKey(n) }
}

export const deployNode = async () => {
  const m = nodeModal.value
  if ((!m.ip && !m.redeployIP) || (!m.password && !m.byKey)) return
  const res = m.redeployIP
    ? await apiCall(`api/nodes/${encodeURIComponent(m.redeployIP)}/redeploy`, { method: 'POST', body: JSON.stringify({ password: m.password }) })
    : await apiCall('api/nodes', { method: 'POST', body: JSON.stringify({ ip: m.ip, type: m.type, password: m.password, sni: m.sni }) })
  const data = await res.json()
  if (!res.ok) {
    alert(data.error || t('deployError'))
    return
  }
  m.message = t('deployStarted')
  m.password = ''
  setTimeout(() => {
    nodeModal.value = null
    fetchNodes()
  }, 3000)
}

const patchNode = async (node, body) => {
  const res = await apiCall(`api/nodes/${encodeURIComponent(node.IP)}`, { method: 'PATCH', body: JSON.stringify(body) })
  if (!res.ok) {
    alert(await readError(res))
    return
  }
  await fetchNodes()
}

export const editSNI = async (node) => {
  const sni = prompt(t('sniPrompt'), node.SNI)
  if (!sni || sni === node.SNI) return
  await patchNode(node, { sni: sni.trim() })
}

export const editLabel = async (node) => {
  const label = prompt(t('labelPrompt'), node.Label || '')
  if (label === null || label.trim() === (node.Label || '')) return
  await patchNode(node, { label: label.trim() })
}

export const editCDN = async (node) => {
  const cdn = prompt(t('cdnPrompt'), node.CDNDomain || '')
  if (cdn === null || cdn.trim() === (node.CDNDomain || '')) return
  await patchNode(node, { cdn: cdn.trim() })
}

export const deleteNode = async (node) => {
  if (!confirm(t('confirmDeleteNode', { ip: node.IP }))) return
  try {
    await apiCall(`api/nodes/${encodeURIComponent(node.IP)}`, { method: 'DELETE' })
    await fetchNodes()
  } catch (error) {
    console.error(error)
  }
}

export const nodeName = (n) => n.Label || n.IP
export const bridges = computed(() => nodes.value.filter(n => n.Type === 'bridge'))
export const exits = computed(() => nodes.value.filter(n => n.Type === 'exit'))
// Одиночный режим: выходных нод нет, мост сам выпускает трафик (и сам ходит в WARP)
export const hasExits = computed(() => exits.value.length > 0)

// ================= НАСТРОЙКИ (маршрутизация, протоколы, обновления) =================
export const routing = ref(null)
export const regions = ref({})
export const fingerprints = ref([])
export const warpTemplates = ref({})
export const warpRulesText = ref('')
export const bridgeDirectText = ref('')
// Хэш агента на мастере: нода с другим хэшем обновится сама при следующей синхронизации
export const agentSHA = ref('')
export const saving = ref(false)
export const saveMessage = ref('')
export const saveError = ref('')

const toLines = (list) => (list || []).join('\n')
const fromLines = (text) => text.split('\n').map(s => s.trim()).filter(Boolean)

export const fetchSettings = async () => {
  if (!token.value) return
  try {
    const res = await apiCall('api/settings')
    const data = await res.json()
    routing.value = data.routing
    regions.value = data.regions
    warpTemplates.value = data.warp_templates
    fingerprints.value = data.fingerprints || []
    agentSHA.value = data.agent_sha || ''
    warpRulesText.value = toLines(data.routing.warp_rules)
    bridgeDirectText.value = toLines(data.routing.bridge_direct)
  } catch (error) {
    console.error(error)
  }
}

export const addTemplate = (name) => {
  const current = fromLines(warpRulesText.value)
  for (const rule of warpTemplates.value[name] || []) {
    if (!current.includes(rule)) current.push(rule)
  }
  warpRulesText.value = toLines(current)
}

// Одни настройки на экранах «Протоколы», «Маршрутизация» и «Настройки»: сохраняем целиком
export const saveSettings = async () => {
  saving.value = true
  saveMessage.value = ''
  saveError.value = ''
  try {
    const res = await apiCall('api/settings', {
      method: 'PUT',
      body: JSON.stringify({
        ...routing.value,
        warp_rules: fromLines(warpRulesText.value),
        bridge_direct: fromLines(bridgeDirectText.value)
      })
    })
    const data = await res.json()
    if (!res.ok) {
      saveError.value = data.error
      return
    }
    routing.value = data
    saveMessage.value = t('saved')
    setTimeout(() => { saveMessage.value = '' }, 2000)
    fetchPreview()
  } catch (error) {
    console.error(error)
  } finally {
    saving.value = false
  }
}

export const latestXray = ref('')
export const latestXrayError = ref('')
export const fetchLatestXray = async () => {
  latestXrayError.value = ''
  const res = await apiCall('api/xray/latest')
  const data = await res.json()
  if (!res.ok) {
    latestXrayError.value = data.error
    return
  }
  latestXray.value = data.version
}

export const agentPending = (n) => !!(agentSHA.value && n.AgentVersion && n.AgentVersion !== agentSHA.value)
export const xrayPending = (n) => !!(routing.value?.xray_version && n.XrayVersion && routing.value.xray_version !== n.XrayVersion)

// Серверы в подписке при сохранённых настройках
export const preview = ref([])
export const fetchPreview = async () => {
  if (!token.value) return
  try {
    const res = await apiCall('api/sub-preview')
    if (res.ok) preview.value = await res.json()
  } catch (error) {
    console.error(error)
  }
}

// ================= СТРАНИЦА ПОДПИСКИ =================
export const page = ref(null)
export const pageDefaults = ref(null)
export const platforms = ref([])

export const fetchPage = async () => {
  if (!token.value) return
  try {
    const res = await apiCall('api/page-settings')
    const data = await res.json()
    page.value = data.page
    pageDefaults.value = data.defaults
    platforms.value = data.platforms
  } catch (error) {
    console.error(error)
  }
}

export const savePage = async () => {
  saving.value = true
  saveMessage.value = ''
  saveError.value = ''
  try {
    const res = await apiCall('api/page-settings', { method: 'PUT', body: JSON.stringify(page.value) })
    const data = await res.json()
    if (!res.ok) {
      saveError.value = data.error
      return
    }
    page.value = data
    saveMessage.value = t('saved')
    setTimeout(() => { saveMessage.value = '' }, 2000)
  } catch (error) {
    console.error(error)
  } finally {
    saving.value = false
  }
}

// ================= КАНАЛ МОСТ → ВЫХОДНЫЕ НОДЫ =================
export const links = ref(null) // { since, step_minutes, alert_pct, links: [{from, to, points, current}] }
export const linksRange = ref('24h')

export const fetchLinks = async () => {
  if (!token.value) return
  try {
    const res = await apiCall(`api/links?range=${linksRange.value}`)
    if (res.ok) links.value = await res.json()
  } catch (error) {
    console.error(error)
  }
}

// Текущее состояние канала до экзита (за последние 10 минут) — для Обзора
export const linkTo = (ip) => links.value?.links?.find(l => l.to === ip)?.current || null

// ================= ПОКАЗАТЕЛИ СЕРВЕРОВ И ДЕЙСТВИЯ =================
export const metrics = ref(null) // { since, step_minutes, alerts, nodes: [{node, points, current, last}] }
export const fetchMetrics = async () => {
  if (!token.value) return
  try {
    const res = await apiCall(`api/metrics?range=${linksRange.value}`)
    if (res.ok) metrics.value = await res.json()
  } catch (error) {
    console.error(error)
  }
}
export const metricsOf = (ip) => metrics.value?.nodes?.find(n => n.node === ip) || null

export const nodeAction = async (node, action) => {
  if (!confirm(t('act.confirm.' + action, { name: nodeName(node) }) + (node.RealityDest && action === 'reboot' ? '\n\n' + t('act.masterWarn') : ''))) return
  const res = await apiCall(`api/nodes/${encodeURIComponent(node.IP)}/action`, { method: 'POST', body: JSON.stringify({ action }) })
  if (!res.ok) {
    alert(await readError(res))
    return
  }
  await fetchNodes()
}

// ================= ДВУХФАКТОРНЫЙ ВХОД =================
export const twofa = ref(null) // { enabled, recovery_left }
export const fetchTwofa = async () => {
  if (!token.value) return
  try {
    const res = await apiCall('api/2fa')
    if (res.ok) twofa.value = await res.json()
  } catch (error) {
    console.error(error)
  }
}
export const twofaPost = async (path, body) => {
  const res = await apiCall(`api/2fa/${path}`, { method: 'POST', body: body ? JSON.stringify(body) : undefined })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || data.message || res.statusText)
  return data
}

// ================= SSH И ТЕРМИНАЛ =================
// SSHState от агента: keys — ключ мастера стоит, keys-only — вход по паролю выключен
export const hasPanelKey = (n) => n.SSHState === 'keys' || n.SSHState === 'keys-only'
export const terminalFor = ref(null) // нода, для которой открыт терминал

export const setKeysOnly = async (node, on) => {
  if (!confirm(t(on ? 'tm.keysOnlyConfirm' : 'tm.keysOnlyOffConfirm', { name: nodeName(node) }))) return
  const res = await apiCall(`api/nodes/${encodeURIComponent(node.IP)}/ssh`, { method: 'POST', body: JSON.stringify({ keys_only: on }) })
  if (!res.ok) alert(await readError(res))
  await fetchNodes()
}

// Пропуск в терминал: свежий код 2FA → одноразовый ticket для WebSocket
export const terminalTicket = async (node, code) => {
  const res = await apiCall(`api/nodes/${encodeURIComponent(node.IP)}/terminal`, { method: 'POST', body: JSON.stringify({ code }) })
  const data = await res.json().catch(() => ({}))
  if (res.status === 429) throw new Error(t('tooManyAttempts'))
  if (!res.ok) throw new Error(data.error || res.statusText)
  return data.ticket
}

export const sshKeys = ref(null) // { master, admin: [] }
export const terminalLog = ref([])
export const fetchSSHKeys = async () => {
  if (!token.value) return
  try {
    const [k, l] = await Promise.all([apiCall('api/ssh-keys'), apiCall('api/terminal/log')])
    if (k.ok) sshKeys.value = await k.json()
    if (l.ok) terminalLog.value = await l.json()
  } catch (error) {
    console.error(error)
  }
}
export const saveSSHKeys = async (text) => {
  const res = await apiCall('api/ssh-keys', { method: 'PUT', body: JSON.stringify({ admin: text }) })
  if (!res.ok) throw new Error(await readError(res))
  sshKeys.value = await res.json()
}

// ================= УВЕДОМЛЕНИЯ В TELEGRAM =================
// form — настройки; token — новый токен (пусто — оставить прежний); status — последняя отправка
export const notify = ref(null)
export const notifyMessage = ref('')
export const notifyError = ref('')

const setNotify = (data) => {
  notify.value = { form: data.settings, hasToken: data.has_token, token: '', status: data.status }
}

export const fetchNotify = async () => {
  if (!token.value) return
  try {
    const res = await apiCall('api/notify')
    if (res.ok) setNotify(await res.json())
  } catch (error) {
    console.error(error)
  }
}

export const saveNotify = async (clearToken = false) => {
  notifyMessage.value = ''
  notifyError.value = ''
  const n = notify.value
  const res = await apiCall('api/notify', {
    method: 'PUT',
    body: JSON.stringify({ ...n.form, token: n.token.trim(), clear_token: clearToken })
  })
  const data = await res.json()
  if (!res.ok) {
    notifyError.value = data.error
    return false
  }
  setNotify(data)
  notifyMessage.value = t('saved')
  setTimeout(() => { notifyMessage.value = '' }, 2000)
  return true
}

export const testNotify = async () => {
  notifyMessage.value = ''
  notifyError.value = ''
  // Несохранённые изменения сначала сохраняем — проверяем то, что видно на экране
  if (!(await saveNotify())) return
  const res = await apiCall('api/notify/test', { method: 'POST' })
  const data = await res.json()
  if (!res.ok) {
    notifyError.value = data.error
    return
  }
  notify.value.status = data
  notifyMessage.value = t('nt.testQueued')
  // Ответ придёт после синхронизации выходной ноды — до минуты
  for (let i = 0; i < 8; i++) {
    await new Promise(r => setTimeout(r, 10000))
    await fetchNotify()
    const last = notify.value?.status?.last
    if (last && (last.sent_at || last.failed || last.error)) break
  }
}

// ================= ОБЗОР =================
const DAY = 86400000

export const overview = computed(() => {
  const u = users.value
  const now = Date.now()
  return {
    nodesOnline: nodes.value.filter(n => n.IsOnline).length,
    nodesTotal: nodes.value.length,
    usersActive: u.filter(x => x.Status === 'active' && !isExpired(x)).length,
    usersTotal: u.length,
    traffic: u.reduce((s, x) => s + used(x), 0),
    expiringSoon: u.filter(x => x.ExpiresAt && !isExpired(x) && new Date(x.ExpiresAt) - now < 7 * DAY).length,
  }
})

// Что требует внимания: ошибки нод, ожидающие обновления, пользователи у лимита
export const attention = computed(() => {
  const out = []
  for (const n of nodes.value) {
    const name = nodeName(n)
    if (!n.IsOnline) out.push({ kind: 'err', title: t('att.offline', { name }), text: t('att.offlineText', { ago: ago(n.LastSeen) }), view: 'nodes' })
    if (n.ConfigError) out.push({ kind: 'err', title: t('att.config', { name }), text: n.ConfigError, view: 'nodes' })
    if (n.UpdateError) out.push({ kind: 'err', title: t('att.update', { name }), text: n.UpdateError, view: 'nodes' })
    if (n.MieruError && routing.value?.mieru) out.push({ kind: 'err', title: t('att.mieru', { name }), text: n.MieruError, view: 'nodes' })
    const m = n.IsOnline ? metricsOf(n.IP)?.current : null
    const lim = metrics.value?.alerts
    if (m && lim) {
      if (m.disk >= lim.disk) out.push({ kind: 'err', title: t('att.disk', { name }), text: t('att.diskText', { v: m.disk }), view: 'nodes' })
      if (m.mem >= lim.mem) out.push({ kind: 'warn', title: t('att.mem', { name }), text: t('att.memText', { v: m.mem }), view: 'nodes' })
      if (m.steal >= lim.steal) out.push({ kind: 'warn', title: t('att.steal', { name }), text: t('att.stealText', { v: m.steal }), view: 'nodes' })
      if (m.retrans >= lim.retrans) out.push({ kind: 'warn', title: t('att.retrans', { name }), text: t('att.retransText', { v: m.retrans }), view: 'nodes' })
      if (m.cpu >= lim.cpu) out.push({ kind: 'warn', title: t('att.cpu', { name }), text: t('att.cpuText', { v: m.cpu }), view: 'nodes' })
    }
    const link = n.Type === 'exit' && n.IsOnline ? linkTo(n.IP) : null
    if (link && link.loss_pct >= (links.value?.alert_pct ?? 10)) out.push({ kind: 'warn', title: t('att.link', { name }), text: t('att.linkText', { loss: link.loss_pct, ms: link.avg_ms }), view: 'nodes' })
    if (n.SNIError) out.push({ kind: 'warn', title: t('att.sni', { name }), text: n.SNIError, view: 'nodes' })
    if (n.WarpError && n.IsOnline) out.push({ kind: 'warn', title: t('att.warp', { name }), text: n.WarpError, view: 'nodes' })
    if (n.IsOnline && agentPending(n)) out.push({ kind: 'acc', title: t('att.agent', { name }), text: t('att.agentText'), view: 'nodes' })
  }
  for (const u of users.value) {
    if (u.Status !== 'active') continue
    const pct = quotaPct(u)
    if (isExpired(u)) out.push({ kind: 'warn', title: t('att.expired', { name: u.Name }), text: t('att.expiredText', { date: fmtDate(u.ExpiresAt) }), view: 'users', user: u })
    else if (pct !== null && pct >= 90) out.push({ kind: pct >= 100 ? 'err' : 'warn', title: t('att.quota', { name: u.Name, pct }), text: t('att.quotaText', { used: fmtGB(used(u)), total: fmtGB(u.TrafficQuota) }), view: 'users', user: u })
  }
  const rank = { err: 0, warn: 1, acc: 2 }
  return out.sort((a, b) => rank[a.kind] - rank[b.kind])
})

// Больше всего трафика — для обзора
export const topUsers = computed(() => {
  const list = [...users.value].sort((a, b) => used(b) - used(a)).slice(0, 5).filter(u => used(u) > 0)
  const max = Math.max(1, ...list.map(used))
  return list.map(u => ({ user: u, pct: quotaPct(u) ?? Math.round((used(u) / max) * 100), limited: !!u.TrafficQuota }))
})

export const loadAll = () => {
  fetchUsers()
  fetchNodes()
  fetchSettings()
  fetchPage()
  fetchPreview()
  fetchNotify()
  fetchLinks()
  fetchMetrics()
  fetchTwofa()
  fetchSSHKeys()
}
