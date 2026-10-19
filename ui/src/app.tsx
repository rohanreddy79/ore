import { useEffect, useMemo } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { ToastBridge, ToastProvider, toast } from './design'
import { onApiError } from './lib/api'
import { createAppRouter } from './router'

export function App() {
  const queryClient = useMemo(() => new QueryClient({ defaultOptions: { queries: { refetchOnWindowFocus: true, staleTime: 5000 } } }), [])
  const router = useMemo(() => createAppRouter(queryClient), [queryClient])

  useEffect(() => {
    return onApiError((e) => {
      if (e.status === 401) {
        const here = window.location.pathname + window.location.search
        if (!here.startsWith('/app/login')) {
          void router.navigate({ to: '/login', search: { next: here.replace(/^\/app/, '') || '/' } })
        }
        return
      }
      if (e.status === 0 || e.status >= 500) {
        toast({ tone: 'danger', title: e.status === 0 ? 'Cannot reach the server' : `Server error (${e.status})`, body: e.message, action: e.retry ? { label: 'Retry', onClick: () => void e.retry?.().then(() => queryClient.invalidateQueries()) } : undefined })
      }
    })
  }, [router, queryClient])

  return (
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <ToastBridge />
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>
  )
}
