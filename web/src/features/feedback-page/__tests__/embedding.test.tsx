/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { FeedbackPage } from '@/features/feedback-page'
import { api } from '@/lib/api'

let queryClient: QueryClient

function Wrapper(props: { children: ReactNode }) {
  return (
    <QueryClientProvider client={queryClient}>
      {props.children}
    </QueryClientProvider>
  )
}

beforeEach(() => {
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
})

afterEach(() => {
  cleanup()
  queryClient.clear()
  vi.restoreAllMocks()
})

describe('feedback page embedding', () => {
  test('shows an empty state when no feedback URL is configured', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { configured: false, url: '' } },
    })

    render(<FeedbackPage />, { wrapper: Wrapper })

    expect(
      await screen.findByText('Feedback page is not configured')
    ).toBeVisible()
    expect(screen.queryByTitle('Feedback')).not.toBeInTheDocument()
  })

  test('accepts readiness only from the configured frame origin', async () => {
    const pageURL = 'https://feedback.example.com/embed'
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { configured: true, url: pageURL } },
    })

    render(<FeedbackPage />, { wrapper: Wrapper })

    const frame = await screen.findByTitle<HTMLIFrameElement>('Feedback')
    expect(frame).toHaveAttribute('src', pageURL)
    expect(frame).toHaveAttribute(
      'sandbox',
      'allow-scripts allow-same-origin allow-forms'
    )
    const frameWindow = frame.contentWindow
    if (!frameWindow) throw new Error('Expected iframe contentWindow')
    const postMessage = vi.spyOn(frameWindow, 'postMessage')
    const readyMessage = { source: 'feedback-page', type: 'ready' }

    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          data: readyMessage,
          origin: 'https://attacker.example',
          source: frameWindow,
        })
      )
    })
    expect(postMessage).not.toHaveBeenCalled()

    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          data: readyMessage,
          origin: 'https://feedback.example.com',
          source: frameWindow,
        })
      )
    })

    await waitFor(() =>
      expect(postMessage).toHaveBeenCalledWith(
        { source: 'new-api', theme: 'light', type: 'theme' },
        'https://feedback.example.com'
      )
    )
  })
})
