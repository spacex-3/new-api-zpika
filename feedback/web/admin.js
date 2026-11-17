const loginView = document.querySelector('#login-view')
const adminView = document.querySelector('#admin-view')
const toastElement = document.querySelector('#toast')
const feedbackList = document.querySelector('#feedback-list')
const feedbackSummary = document.querySelector('#feedback-summary')
const statusFilter = document.querySelector('#status-filter')
let csrfToken = ''
let feedbacks = []
let toastTimer

function showToast(message) {
  toastElement.textContent = message
  toastElement.classList.add('visible')
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => toastElement.classList.remove('visible'), 2800)
}

function escapeHTML(value) {
  return String(value ?? '').replace(/[&<>"']/g, (character) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[character])
}

async function api(path, options = {}) {
  const headers = { ...(options.headers || {}) }
  if (options.body) headers['Content-Type'] = 'application/json'
  if (csrfToken && options.method && options.method !== 'GET') headers['X-CSRF-Token'] = csrfToken
  const response = await fetch(path, { credentials: 'same-origin', ...options, headers })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(data.error || `请求失败（${response.status}）`)
  return data
}

function showLogin() {
  csrfToken = ''
  adminView.hidden = true
  loginView.hidden = false
}

function showAdmin() {
  loginView.hidden = true
  adminView.hidden = false
}

function formatTime(value) {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}

const statusNames = { new: '待处理', processing: '处理中', resolved: '已解决' }

function renderFeedbacks() {
  const filter = statusFilter.value
  const visible = filter === 'all' ? feedbacks : feedbacks.filter((item) => item.status === filter)
  const pending = feedbacks.filter((item) => item.status !== 'resolved').length
  feedbackSummary.textContent = `共 ${feedbacks.length} 条，${pending} 条待完成`
  if (!visible.length) {
    feedbackList.innerHTML = '<div class="empty-state">当前没有符合条件的反馈</div>'
    return
  }
  feedbackList.innerHTML = visible.map((item) => `
    <article class="feedback-item">
      <div class="feedback-meta">
        <div><span class="status status-${escapeHTML(item.status)}">${statusNames[item.status] || item.status}</span><strong>${escapeHTML(item.email)}</strong></div>
        <time>${formatTime(item.created_at)}</time>
      </div>
      <p class="feedback-content">${escapeHTML(item.content)}</p>
      <div class="feedback-actions">
        <span class="notification-state ${item.notification_delivered ? 'delivered' : 'failed'}">
          ${item.notification_delivered ? 'Bark 已送达' : `Bark 未送达${item.notification_error ? `：${escapeHTML(item.notification_error)}` : ''}`}
        </span>
        <select data-feedback-id="${escapeHTML(item.id)}" aria-label="更新反馈状态">
          ${Object.entries(statusNames).map(([value, label]) => `<option value="${value}"${item.status === value ? ' selected' : ''}>${label}</option>`).join('')}
        </select>
      </div>
    </article>`).join('')
  document.querySelectorAll('[data-feedback-id]').forEach((select) => {
    select.addEventListener('change', async () => {
      select.disabled = true
      try {
        const data = await api(`/api/admin/feedbacks/${encodeURIComponent(select.dataset.feedbackId)}`, {
          method: 'PUT', body: JSON.stringify({ status: select.value }),
        })
        const index = feedbacks.findIndex((item) => item.id === data.feedback.id)
        if (index >= 0) feedbacks[index] = data.feedback
        renderFeedbacks()
        showToast('状态已更新')
      } catch (error) {
        showToast(error.message)
        await loadFeedbacks()
      } finally {
        select.disabled = false
      }
    })
  })
}

async function loadFeedbacks() {
  const data = await api('/api/admin/feedbacks')
  feedbacks = data.feedbacks || []
  renderFeedbacks()
}

async function loadSettings() {
  const data = await api('/api/admin/settings')
  document.querySelector('#bark-url').placeholder = data.bark_configured ? '已配置，输入新地址可替换' : 'https://api.day.app/你的密钥'
}

async function initialize() {
  try {
    const session = await api('/api/admin/session')
    csrfToken = session.csrf_token
    showAdmin()
    await Promise.all([loadFeedbacks(), loadSettings()])
  } catch {
    showLogin()
  }
}

document.querySelector('#login-form').addEventListener('submit', async (event) => {
  event.preventDefault()
  const button = document.querySelector('#login-button')
  const errorElement = document.querySelector('#login-error')
  button.disabled = true
  errorElement.textContent = ''
  try {
    const data = await api('/api/admin/login', {
      method: 'POST', body: JSON.stringify({ token: document.querySelector('#admin-token').value }),
    })
    csrfToken = data.csrf_token
    document.querySelector('#admin-token').value = ''
    showAdmin()
    await Promise.all([loadFeedbacks(), loadSettings()])
  } catch (error) {
    errorElement.textContent = error.message
  } finally {
    button.disabled = false
  }
})

document.querySelector('#logout-button').addEventListener('click', async () => {
  try { await api('/api/admin/logout', { method: 'POST' }) } finally { showLogin() }
})

document.querySelectorAll('[data-tab]').forEach((button) => {
  button.addEventListener('click', () => {
    document.querySelectorAll('[data-tab]').forEach((item) => item.classList.toggle('active', item === button))
    document.querySelector('#feedbacks-tab').hidden = button.dataset.tab !== 'feedbacks'
    document.querySelector('#settings-tab').hidden = button.dataset.tab !== 'settings'
  })
})

statusFilter.addEventListener('change', renderFeedbacks)

document.querySelector('#settings-form').addEventListener('submit', async (event) => {
  event.preventDefault()
  const barkURL = document.querySelector('#bark-url')
  try {
    await api('/api/admin/settings', { method: 'PUT', body: JSON.stringify({ bark_url: barkURL.value.trim() }) })
    barkURL.value = ''
    await loadSettings()
    showToast('Bark 配置已保存')
  } catch (error) {
    showToast(error.message)
  }
})

document.querySelector('#test-notification').addEventListener('click', async (event) => {
  event.currentTarget.disabled = true
  try {
    await api('/api/admin/notifications/test', { method: 'POST' })
    showToast('测试通知已发送')
  } catch (error) {
    showToast(error.message)
  } finally {
    event.currentTarget.disabled = false
  }
})

initialize()
