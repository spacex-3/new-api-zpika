/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

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

import { DegradationMonitor } from '@/features/degradation-monitor'
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

describe('degradation monitor embedding', () => {
  test('shows an empty state when no monitor URL is configured', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { configured: false, url: '' } },
    })

    render(<DegradationMonitor />, { wrapper: Wrapper })

    expect(
      await screen.findByText('Degradation monitor is not configured')
    ).toBeVisible()
    expect(screen.queryByTitle('Degradation Monitor')).not.toBeInTheDocument()
  })

  test('accepts readiness only from the configured frame origin', async () => {
    const monitorURL = 'https://monitor.example.com/embed'
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { configured: true, url: monitorURL } },
    })

    render(<DegradationMonitor />, { wrapper: Wrapper })

    const frame = await screen.findByTitle<HTMLIFrameElement>(
      'Degradation Monitor'
    )
    expect(frame).toHaveAttribute('src', monitorURL)
    expect(frame).toHaveAttribute('referrerpolicy', 'strict-origin')
    const frameWindow = frame.contentWindow
    if (!frameWindow) throw new Error('Expected iframe contentWindow')
    const postMessage = vi.spyOn(frameWindow, 'postMessage')
    const readyMessage = {
      source: 'degradation-monitor',
      type: 'ready',
    }

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
          origin: 'https://monitor.example.com',
          source: frameWindow,
        })
      )
    })

    await waitFor(() =>
      expect(postMessage).toHaveBeenCalledWith(
        { source: 'new-api', theme: 'light', type: 'theme' },
        'https://monitor.example.com'
      )
    )
  })
})
