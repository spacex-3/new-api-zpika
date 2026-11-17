const form = document.querySelector('#feedback-form')
const email = document.querySelector('#email')
const content = document.querySelector('#content')
const website = document.querySelector('#website')
const submitButton = document.querySelector('#submit-button')
const successState = document.querySelector('#success-state')
const feedbackId = document.querySelector('#feedback-id')
const contentCount = document.querySelector('#content-count')
const toastElement = document.querySelector('#toast')
let toastTimer

function showToast(message) {
  toastElement.textContent = message
  toastElement.classList.add('visible')
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => toastElement.classList.remove('visible'), 2800)
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
    const response = await fetch('/api/feedback', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email: email.value.trim(), content: content.value.trim(), website: website.value }),
    })
    const data = await response.json().catch(() => ({}))
    if (!response.ok || !data.ok) throw new Error(data.error || '提交失败，请稍后再试')
    form.hidden = true
    successState.hidden = false
    feedbackId.textContent = data.id || '已记录'
  } catch (error) {
    showToast(error.message || '网络异常，请稍后再试')
  } finally {
    submitButton.disabled = false
    submitButton.textContent = '提交反馈'
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
