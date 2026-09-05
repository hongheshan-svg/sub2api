/**
 * Admin Kiro (Amazon Q Developer / CodeWhisperer) OAuth API endpoints.
 *
 * 对应后端 Task 14 的四个管理端点（`internal/handler/admin/kiro_oauth_handler.go`）：
 * authorize-url（IdC 授权码）、device/start + device/poll（Builder ID 设备码）、
 * credentials/:session_id（IdC 回调落地后前端轮询取回一次性凭据）。
 *
 * `/admin/kiro/oauth/callback` 不在此列——它是浏览器整页跳转落地的 HTML
 * 页面，不是 JS fetch 调用的 JSON API。
 */

import { apiClient } from '../client'

export interface KiroAuthorizeUrlRequest {
  proxy_id?: number | null
  redirect_uri: string
  issuer_url: string
  region?: string
}

export interface KiroAuthorizeUrlResponse {
  session_id: string
  authorize_url: string
  expires_in: number
}

export interface KiroDeviceStartRequest {
  proxy_id?: number | null
  region?: string
}

export interface KiroDeviceStartResponse {
  session_id: string
  user_code: string
  verification_uri: string
  verification_uri_complete: string
  expires_in: number
  interval: number
}

export interface KiroDevicePollRequest {
  session_id: string
  proxy_id?: number | null
}

export type KiroOAuthPollStatus = 'pending' | 'ok'

/** 与后端 KiroOAuthService.BuildAccountCredentials 的输出一一对应（snake_case）。 */
export interface KiroRawCredentials {
  auth_method?: string
  access_token?: string
  refresh_token?: string
  profile_arn?: string
  expires_at?: string
  region?: string
  issuer_url?: string
  client_id?: string
  client_secret?: string
  machine_id?: string
  [key: string]: unknown
}

export interface KiroDevicePollResponse {
  status: KiroOAuthPollStatus
  interval?: number
  credentials?: KiroRawCredentials
}

export interface KiroFetchCredentialsResponse {
  status: KiroOAuthPollStatus
  credentials?: KiroRawCredentials
}

export async function authorizeUrl(
  payload: KiroAuthorizeUrlRequest
): Promise<KiroAuthorizeUrlResponse> {
  const { data } = await apiClient.post<KiroAuthorizeUrlResponse>(
    '/admin/kiro/oauth/authorize-url',
    payload
  )
  return data
}

export async function deviceStart(
  payload: KiroDeviceStartRequest
): Promise<KiroDeviceStartResponse> {
  const { data } = await apiClient.post<KiroDeviceStartResponse>(
    '/admin/kiro/oauth/device/start',
    payload
  )
  return data
}

export async function devicePoll(
  payload: KiroDevicePollRequest
): Promise<KiroDevicePollResponse> {
  const { data } = await apiClient.post<KiroDevicePollResponse>(
    '/admin/kiro/oauth/device/poll',
    payload
  )
  return data
}

export async function fetchCredentials(sessionId: string): Promise<KiroFetchCredentialsResponse> {
  const { data } = await apiClient.get<KiroFetchCredentialsResponse>(
    `/admin/kiro/oauth/credentials/${encodeURIComponent(sessionId)}`
  )
  return data
}

export default {
  authorizeUrl,
  deviceStart,
  devicePoll,
  fetchCredentials
}
