import {
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient
} from '@tanstack/react-query'

import { apiDelete, apiGet, apiPost, apiPut } from '@/api/client'
import { resetMovieStates } from '@/api/movie-states'
import { taskKeys } from '@/api/tasks'

export type PanDirectory = {
  id: string
  name: string
  path: string
}

type PanSpaceAmount = {
  bytes: number
  formatted: string
}

export type PanAccount = {
  id: string
  name: string
  avatar: string
  level: string
  space: {
    total: PanSpaceAmount
    used: PanSpaceAmount
    remaining: PanSpaceAmount
  }
}

export type PanAccountStatus = {
  connected: boolean
  account?: PanAccount
  directory?: PanDirectory
}

export type PanFilePage = {
  files: { id: string; parent_id: string; name: string; is_directory: boolean; size: number; sha1: string }[]
  path: { id: string; name: string }[]
  total: number
  has_more: boolean
}

export type PanSidecarSyncConfig = {
  enabled: boolean
  account_id: string
  parent_id: string
  destination: string
  child_directories: PanDirectory[]
  interval_minutes: number
  last_run_at?: string
  last_result?: string
  files_downloaded: number
  files_skipped: number
  errors?: string[]
}

export type PanSidecarSyncUpdate = Pick<
  PanSidecarSyncConfig,
  'enabled' | 'destination' | 'child_directories' | 'interval_minutes'
>

export type PanLoginSession = {
  id: string
  qr_code: string
}

export type PanLoginState = 'waiting' | 'scanned' | 'authorized' | 'expired' | 'canceled'

type PanLoginStatus = {
  state: PanLoginState
}

export const panKeys = {
  account: ['pan', 'account'] as const,
  login: (id: string) => ['pan', 'login', id] as const,
  fileLists: ['pan', 'files'] as const,
  sidecarSync: ['pan', 'sidecar-sync'] as const,
  files: (accountID: string, directoryID: string, page: number) =>
    ['pan', 'files', accountID, directoryID, page] as const
}

export function usePanSidecarSyncConfig() {
  return useQuery({
    queryKey: panKeys.sidecarSync,
    queryFn: ({ signal }) =>
      apiGet<PanSidecarSyncConfig>('/api/pan/sidecar-sync', undefined, signal),
    refetchOnMount: 'always'
  })
}

export function useUpdatePanSidecarSync() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (config: PanSidecarSyncUpdate) =>
      apiPut<PanSidecarSyncConfig>('/api/pan/sidecar-sync', config),
    onSuccess: config => queryClient.setQueryData(panKeys.sidecarSync, config)
  })
}

export function useRunPanSidecarSync() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => apiPost<PanSidecarSyncConfig>('/api/pan/sidecar-sync/run'),
    onSuccess: config => queryClient.setQueryData(panKeys.sidecarSync, config),
    onError: () => void queryClient.invalidateQueries({ queryKey: panKeys.sidecarSync })
  })
}

export function invalidatePanSource(queryClient: QueryClient) {
  void queryClient.invalidateQueries({ queryKey: ['library'] })
  void queryClient.invalidateQueries({ queryKey: ['offline'] })
  void resetMovieStates(queryClient)
}

export function usePanAccount(enabled = true) {
  return useQuery({
    queryKey: panKeys.account,
    queryFn: ({ signal }) => apiGet<PanAccountStatus>('/api/pan/account', undefined, signal),
    enabled
  })
}

export function useBeginPanLogin() {
  return useMutation({
    mutationFn: () => apiPost<PanLoginSession>('/api/pan/login'),
    gcTime: 0
  })
}

const loginPollInterval = 1500

export function panLoginOptions(id: string) {
  return queryOptions({
    queryKey: panKeys.login(id),
    queryFn: ({ signal }) =>
      apiGet<PanLoginStatus>(`/api/pan/login/${encodeURIComponent(id)}`, undefined, signal),
    enabled: id !== '',
    // Four retries plus the first attempt preserve the five-failure budget.
    retry: 4,
    retryDelay: loginPollInterval,
    staleTime: 0,
    gcTime: 0,
    refetchOnReconnect: false,
    refetchInterval: query => {
      if (query.state.status === 'error') return false
      const state = query.state.data?.state
      return !state || state === 'waiting' || state === 'scanned' ? loginPollInterval : false
    }
  })
}

export function usePanLoginStatus(id: string) {
  return useQuery(panLoginOptions(id))
}

export function useDisconnectPan() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => apiDelete<PanAccountStatus>('/api/pan/account'),
    onSuccess: async status => {
      queryClient.setQueryData(panKeys.account, status)
      invalidatePanSource(queryClient)
      await queryClient.cancelQueries({ queryKey: panKeys.fileLists })
      queryClient.removeQueries({ queryKey: panKeys.fileLists })
    }
  })
}

export function usePanFiles(accountID: string, directoryID: string, page: number) {
  return useQuery({
    queryKey: panKeys.files(accountID, directoryID, page),
    queryFn: ({ signal }) =>
      apiGet<PanFilePage>('/api/pan/files', { directory_id: directoryID, page }, signal)
  })
}

export function useSelectPanDirectory(accountID: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => apiPut<PanDirectory>('/api/pan/directory', { id }),
    onSuccess: directory => {
      invalidatePanSource(queryClient)
      void queryClient.invalidateQueries({ queryKey: taskKeys.all })
      queryClient.setQueryData<PanAccountStatus>(panKeys.account, status =>
        status?.account?.id === accountID ? { ...status, directory } : status
      )
    }
  })
}

export function useClearPanDirectory(accountID: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => apiDelete<null>('/api/pan/directory'),
    onSuccess: () => {
      invalidatePanSource(queryClient)
      queryClient.setQueryData<PanAccountStatus>(panKeys.account, status =>
        status?.account?.id === accountID ? { ...status, directory: undefined } : status
      )
    }
  })
}
