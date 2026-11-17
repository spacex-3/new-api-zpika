const form = document.querySelector('#feedback-form')
const email = document.querySelector('#email')
const content = document.querySelector('#content')
const website = document.querySelector('#website')
const submitButton = document.querySelector('#submit-button')
const successState = document.querySelector('#success-state')
const feedbackId = document.querySelector('#feedback-id')
const contentCount = document.querySelector('#content-count')
const lookupForm = document.querySelector('#lookup-form')
const lookupEmail = document.querySelector('#lookup-email')
const lookupButton = document.querySelector('#lookup-button')
const lookupError = document.querySelector('#lookup-error')
const historySummary = document.querySelector('#history-summary')
const historyList = document.querySelector('#history-list')
const toastElement = document.querySelector('#toast')
let activeEmail = ''
let feedbacks = []
let toastTimer

const statusNames = { new: '待处理', processing: '处理中', resolved: '已解决' }

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

function formatTime(value) {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}

async function api(path, body) {
  const response = await fetch(path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(data.error || `请求失败（${response.status}）`)
  return data
}

function setError(name, message) {
  const element = document.querySelector(`#${name}-error`)
  const input = document.querySelector(`#${name}`)
  element.textContent = message
  input.setAttribute('aria-invalid', message ? 'true' : 'false')
}

function validate() {
  setError('email', '')
  setError('content', '')
  if (!email.validity.valid) setError('email', '请输入有效的联系邮箱')
  const size = [...content.value.trim()].length
  if (size < 10) setError('content', '反馈内容至少需要 10 个字')
  if (size > 4000) setError('content', '反馈内容不能超过 4000 个字')
  return email.validity.valid && size >= 10 && size <= 4000
}

function renderReplies(replies = []) {
  if (!replies.length) return '<p class="no-replies">管理员暂未回复</p>'
  return `<div class="reply-list">${replies.map((reply) => `
    <div class="reply reply-${escapeHTML(reply.author)}">
      <div class="reply-meta"><strong>${reply.author === 'admin' ? '管理员回复' : '我的追问'}</strong><time>${formatTime(reply.created_at)}</time></div>
      <p>${escapeHTML(reply.content)}</p>
    </div>`).join('')}</div>`
}

function renderHistory() {
  historySummary.hidden = false
  historySummary.textContent = feedbacks.length ? `共查询到 ${feedbacks.length} 条留言` : '没有查询到该邮箱的留言'
  if (!feedbacks.length) {
    historyList.innerHTML = '<div class="empty-state">确认邮箱无误后可以重新查询，或在上方提交新留言。</div>'
    return
  }
  historyList.innerHTML = feedbacks.map((item) => `
    <article class="feedback-item public-feedback-item">
      <div class="feedback-meta">
        <div><span class="status status-${escapeHTML(item.status)}">${statusNames[item.status] || item.status}</span><strong>${escapeHTML(item.id)}</strong></div>
        <time>${formatTime(item.created_at)}</time>
      </div>
      <p class="feedback-content">${escapeHTML(item.content)}</p>
      ${renderReplies(item.replies)}
      <form class="reply-form" data-client-reply="${escapeHTML(item.id)}">
        <label for="reply-${escapeHTML(item.id)}">继续回复</label>
        <textarea id="reply-${escapeHTML(item.id)}" maxlength="4000" required placeholder="补充问题或回复管理员"></textarea>
        <div class="reply-form-footer"><span>最多 4000 个字</span><button type="submit">发送回复</button></div>
      </form>
    </article>`).join('')
}

async function loadHistory(value) {
  lookupError.textContent = ''
  lookupEmail.value = value
  if (!lookupEmail.validity.valid) {
    lookupError.textContent = '请输入有效的联系邮箱'
    lookupEmail.setAttribute('aria-invalid', 'true')
    return false
  }
  lookupEmail.setAttribute('aria-invalid', 'false')
  lookupButton.disabled = true
  lookupButton.textContent = '正在查询...'
  try {
    const normalizedEmail = value.trim().toLowerCase()
    const data = await api('/api/feedbacks/lookup', { email: normalizedEmail })
    activeEmail = normalizedEmail
    feedbacks = data.feedbacks || []
    renderHistory()
    return true
  } catch (error) {
    lookupError.textContent = error.message
    return false
  } finally {
    lookupButton.disabled = false
    lookupButton.textContent = '查询留言'
  }
}

content.addEventListener('input', () => {
  contentCount.textContent = `${[...content.value].length} / 4000`
  if (content.getAttribute('aria-invalid') === 'true') validate()
})
email.addEventListener('input', () => {
  if (email.getAttribute('aria-invalid') === 'true') validate()
})

form.addEventListener('submit', async (event) => {
  event.preventDefault()
  if (!validate()) return
  submitButton.disabled = true
  submitButton.textContent = '正在提交...'
  try {
    const submittedEmail = email.value.trim().toLowerCase()
    const data = await api('/api/feedback', {
      email: submittedEmail, content: content.value.trim(), website: website.value,
    })
    if (!data.ok) throw new Error(data.error || '提交失败，请稍后再试')
    form.hidden = true
    successState.hidden = false
    feedbackId.textContent = data.id || '已记录'
    await loadHistory(submittedEmail)
  } catch (error) {
    showToast(error.message || '网络异常，请稍后再试')
  } finally {
    submitButton.disabled = false
    submitButton.textContent = '提交反馈'
  }
})

lookupForm.addEventListener('submit', async (event) => {
  event.preventDefault()
  await loadHistory(lookupEmail.value)
})

historyList.addEventListener('submit', async (event) => {
  const replyForm = event.target.closest('[data-client-reply]')
  if (!replyForm) return
  event.preventDefault()
  const textarea = replyForm.querySelector('textarea')
  const button = replyForm.querySelector('button')
  const reply = textarea.value.trim()
  if (!reply) {
    showToast('请输入回复内容')
    textarea.focus()
    return
  }
  button.disabled = true
  button.textContent = '发送中...'
  try {
    await api(`/api/feedbacks/${encodeURIComponent(replyForm.dataset.clientReply)}/replies`, {
      email: activeEmail, content: reply,
    })
    textarea.value = ''
    await loadHistory(activeEmail)
    showToast('回复已发送')
  } catch (error) {
    showToast(error.message)
  } finally {
    button.disabled = false
    button.textContent = '发送回复'
  }
})

document.querySelector('#submit-another').addEventListener('click', () => {
  content.value = ''
  contentCount.textContent = '0 / 4000'
  successState.hidden = true
  form.hidden = false
  content.focus()
})

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme === 'dark' ? 'dark' : 'light'
}

window.addEventListener('message', (event) => {
  if (event.source !== window.parent || event.data?.source !== 'new-api' || event.data?.type !== 'theme') return
  applyTheme(event.data.theme)
})

if (window.parent !== window) {
  window.parent.postMessage({ source: 'feedback-page', type: 'ready' }, '*')
}
