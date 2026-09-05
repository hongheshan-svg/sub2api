/**
 * Kiro 账号凭证构造。
 *
 * 字段名必须与后端 credentials schema 严格一致
 * （backend/internal/service/kiro_credentials.go）——
 * 命名漂移会让账号建得出来但转发时取不到凭证，症状是 401 而非表单错误。
 */

export type KiroAuthMethod = 'social' | 'builder_id' | 'idc' | 'api_key'

export const KIRO_AUTH_METHOD_OPTIONS: readonly { value: KiroAuthMethod; label: string }[] = [
  { value: 'social', label: 'Social（粘贴 refreshToken）' },
  { value: 'builder_id', label: 'AWS Builder ID（设备码授权）' },
  { value: 'idc', label: 'IAM Identity Center（组织 SSO）' },
  { value: 'api_key', label: 'Kiro API Key' }
] as const

export interface KiroCredentialForm {
  authMethod: KiroAuthMethod
  refreshToken: string
  accessToken: string
  clientId: string
  clientSecret: string
  issuerUrl: string
  region: string
  profileArn: string
  apiKey: string
  fakeThinking: boolean
}

const trim = (v: string | undefined): string => (v ?? '').trim()

/** 按接入方式返回必填字段名，供表单标星与提交前校验共用。 */
export function kiroRequiredFields(method: KiroAuthMethod): string[] {
  switch (method) {
    case 'api_key':
      return ['apiKey']
    case 'idc':
      return ['refreshToken', 'clientId', 'clientSecret', 'issuerUrl']
    case 'builder_id':
      return ['refreshToken', 'clientId', 'clientSecret']
    default:
      return ['refreshToken']
  }
}

/** 校验表单，返回错误提示；通过时返回 null。 */
export function validateKiroCredentials(form: KiroCredentialForm): string | null {
  const labels: Record<string, string> = {
    refreshToken: 'Refresh Token',
    clientId: 'Client ID',
    clientSecret: 'Client Secret',
    issuerUrl: 'SSO 门户地址',
    apiKey: 'API Key'
  }

  for (const field of kiroRequiredFields(form.authMethod)) {
    if (!trim((form as unknown as Record<string, string>)[field])) {
      return `请填写 ${labels[field] ?? field}`
    }
  }
  return null
}

/**
 * 构造提交给后端的 credentials。
 *
 * 只提交当前接入方式实际需要的字段，避免残留字段让后端的 auth_method
 * 分派产生歧义。machine_id 一律不提交 —— 它由后端在建号时固化，
 * 前端插手会破坏设备指纹的稳定性。
 */
export function buildKiroCredentials(form: KiroCredentialForm): Record<string, unknown> {
  const creds: Record<string, unknown> = { auth_method: form.authMethod }

  const region = trim(form.region)
  if (region) creds.region = region

  const profileArn = trim(form.profileArn)
  if (profileArn && form.authMethod !== 'api_key') creds.profile_arn = profileArn

  if (form.authMethod === 'api_key') {
    creds.api_key = trim(form.apiKey)
  } else {
    creds.refresh_token = trim(form.refreshToken)

    const accessToken = trim(form.accessToken)
    if (accessToken) creds.access_token = accessToken

    if (form.authMethod === 'idc' || form.authMethod === 'builder_id') {
      creds.client_id = trim(form.clientId)
      creds.client_secret = trim(form.clientSecret)
    }
    if (form.authMethod === 'idc') {
      creds.issuer_url = trim(form.issuerUrl)
    }
  }

  // 假思考默认关闭，只在显式开启时提交。
  if (form.fakeThinking) creds.fake_thinking = true

  return creds
}
