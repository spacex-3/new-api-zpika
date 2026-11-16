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
/* eslint-disable react/iframe-missing-sandbox -- The cross-origin monitor needs scripts and same-origin API requests; its CSP allowlists the parent origin. */
import { useQuery } from '@tanstack/react-query'
import { ExternalLink } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { useTheme } from '@/context/theme-provider'

import { getDegradationMonitorConfig } from './api'

const READY_TIMEOUT_MS = 10_000

function getMonitorOrigin(url: string): string {
  try {
    return new URL(url).origin
  } catch {
    return ''
  }
}

export function DegradationMonitor() {
  const { t } = useTranslation()
  const { resolvedTheme } = useTheme()
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [ready, setReady] = useState(false)
  const [timedOut, setTimedOut] = useState(false)
  const [reloadKey, setReloadKey] = useState(0)
  const configQuery = useQuery({
    queryKey: ['degradation-monitor'],
    queryFn: getDegradationMonitorConfig,
    staleTime: 5 * 60 * 1000,
  })
  const monitorURL = configQuery.data?.url ?? ''
  const monitorOrigin = getMonitorOrigin(monitorURL)

  useEffect(() => {
    if (!monitorOrigin) return
    const receiveMessage = (event: MessageEvent) => {
      if (
        event.origin !== monitorOrigin ||
        event.source !== iframeRef.current?.contentWindow ||
        event.data?.source !== 'degradation-monitor' ||
        event.data?.type !== 'ready'
      ) {
        return
      }
      setReady(true)
      setTimedOut(false)
    }
    window.addEventListener('message', receiveMessage)
    return () => window.removeEventListener('message', receiveMessage)
  }, [monitorOrigin, reloadKey])

  useEffect(() => {
    if (!monitorOrigin || ready) return
    const timeout = window.setTimeout(() => setTimedOut(true), READY_TIMEOUT_MS)
    return () => window.clearTimeout(timeout)
  }, [monitorOrigin, ready, reloadKey])

  useEffect(() => {
    if (!ready || !monitorOrigin) return
    iframeRef.current?.contentWindow?.postMessage(
      { source: 'new-api', type: 'theme', theme: resolvedTheme },
      monitorOrigin
    )
  }, [monitorOrigin, ready, resolvedTheme])

  const retry = () => {
    setReady(false)
    setTimedOut(false)
    setReloadKey((value) => value + 1)
  }

  if (configQuery.isPending) {
    return <LoadingState className='h-full' />
  }
  if (configQuery.isError) {
    return (
      <ErrorState className='h-full' onRetry={() => configQuery.refetch()} />
    )
  }
  if (!configQuery.data?.configured || !monitorURL) {
    return (
      <EmptyState
        className='h-full'
        title={t('Degradation monitor is not configured')}
        description={t('Configure the monitor URL in system settings.')}
      />
    )
  }
  if (timedOut) {
    return (
      <ErrorState
        className='h-full'
        title={t('Unable to load the degradation monitor')}
        description={t('Check the monitor service and embedding settings.')}
        onRetry={retry}
        action={
          <Button
            variant='outline'
            size='sm'
            render={<a href={monitorURL} target='_blank' rel='noreferrer' />}
          >
            <ExternalLink />
            {t('Open in new tab')}
          </Button>
        }
      />
    )
  }

  return (
    <div className='relative h-full min-h-0 overflow-hidden'>
      {!ready && <LoadingState className='absolute inset-0' />}
      <iframe
        key={reloadKey}
        ref={iframeRef}
        className='h-full w-full border-0'
        src={monitorURL}
        title={t('Degradation Monitor')}
        sandbox='allow-scripts allow-same-origin'
        referrerPolicy='strict-origin'
      />
    </div>
  )
}
