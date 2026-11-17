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
/* eslint-disable react/iframe-missing-sandbox -- The cross-origin feedback page needs scripts and same-origin API requests; its CSP allowlists the parent origin. */
import { useQuery } from '@tanstack/react-query'
import { ExternalLink } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { useTheme } from '@/context/theme-provider'

import { getFeedbackPageConfig } from './api'

const READY_TIMEOUT_MS = 10_000

function getPageOrigin(url: string): string {
  try {
    return new URL(url).origin
  } catch {
    return ''
  }
}

export function FeedbackPage() {
  const { t } = useTranslation()
  const { resolvedTheme } = useTheme()
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [ready, setReady] = useState(false)
  const [timedOut, setTimedOut] = useState(false)
  const [reloadKey, setReloadKey] = useState(0)
  const configQuery = useQuery({
    queryKey: ['feedback-page'],
    queryFn: getFeedbackPageConfig,
    staleTime: 5 * 60 * 1000,
  })
  const pageURL = configQuery.data?.url ?? ''
  const pageOrigin = getPageOrigin(pageURL)

  useEffect(() => {
    if (!pageOrigin) return
    const receiveMessage = (event: MessageEvent) => {
      if (
        event.origin !== pageOrigin ||
        event.source !== iframeRef.current?.contentWindow ||
        event.data?.source !== 'feedback-page' ||
        event.data?.type !== 'ready'
      ) {
        return
      }
      setReady(true)
      setTimedOut(false)
    }
    window.addEventListener('message', receiveMessage)
    return () => window.removeEventListener('message', receiveMessage)
  }, [pageOrigin, reloadKey])

  useEffect(() => {
    if (!pageOrigin || ready) return
    const timeout = window.setTimeout(() => setTimedOut(true), READY_TIMEOUT_MS)
    return () => window.clearTimeout(timeout)
  }, [pageOrigin, ready, reloadKey])

  useEffect(() => {
    if (!ready || !pageOrigin) return
    iframeRef.current?.contentWindow?.postMessage(
      { source: 'new-api', type: 'theme', theme: resolvedTheme },
      pageOrigin
    )
  }, [pageOrigin, ready, resolvedTheme])

  const retry = () => {
    setReady(false)
    setTimedOut(false)
    setReloadKey((value) => value + 1)
  }

  if (configQuery.isPending) return <LoadingState className='h-full' />
  if (configQuery.isError) {
    return <ErrorState className='h-full' onRetry={configQuery.refetch} />
  }
  if (!configQuery.data?.configured || !pageURL) {
    return (
      <EmptyState
        className='h-full'
        title={t('Feedback page is not configured')}
        description={t('Configure the feedback page URL in system settings.')}
      />
    )
  }
  if (timedOut) {
    return (
      <ErrorState
        className='h-full'
        title={t('Unable to load the feedback page')}
        description={t('Check the feedback service and embedding settings.')}
        onRetry={retry}
        action={
          <Button
            variant='outline'
            size='sm'
            render={<a href={pageURL} target='_blank' rel='noreferrer' />}
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
        src={pageURL}
        title={t('Feedback')}
        sandbox='allow-scripts allow-same-origin allow-forms'
        referrerPolicy='strict-origin'
      />
    </div>
  )
}
