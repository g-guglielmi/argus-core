// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// WebAuthn/passkey helpers. The server speaks base64url JSON (go-webauthn); the browser
// credential API speaks ArrayBuffers, so we convert on the way in and out.

function b64urlToBuf(s: string): ArrayBuffer {
  s = s.replace(/-/g, '+').replace(/_/g, '/')
  const pad = s.length % 4
  if (pad) s += '='.repeat(4 - pad)
  const bin = atob(s)
  const b = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) b[i] = bin.charCodeAt(i)
  return b.buffer
}

function bufToB64url(buf: ArrayBuffer): string {
  const b = new Uint8Array(buf)
  let s = ''
  for (let i = 0; i < b.length; i++) s += String.fromCharCode(b[i])
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

async function errMsg(res: Response, fallback: string): Promise<string> {
  const j = await res.json().catch(() => ({}))
  return (j && j.error) || fallback
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function registerPasskey(name: string): Promise<void> {
  const beginRes = await fetch('/api/me/passkeys/register/begin', { method: 'POST' })
  if (!beginRes.ok) throw new Error(await errMsg(beginRes, 'Could not start passkey setup'))
  const { options, session_token } = await beginRes.json()
  const pk = options.publicKey
  pk.challenge = b64urlToBuf(pk.challenge)
  pk.user.id = b64urlToBuf(pk.user.id)
  if (pk.excludeCredentials) pk.excludeCredentials = pk.excludeCredentials.map((c: any) => ({ ...c, id: b64urlToBuf(c.id) }))

  const cred = (await navigator.credentials.create({ publicKey: pk })) as PublicKeyCredential
  const resp = cred.response as AuthenticatorAttestationResponse
  const body = {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      attestationObject: bufToB64url(resp.attestationObject),
      clientDataJSON: bufToB64url(resp.clientDataJSON),
      transports: (resp as any).getTransports ? (resp as any).getTransports() : undefined,
    },
  }
  const finishRes = await fetch('/api/me/passkeys/register/finish?name=' + encodeURIComponent(name), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-WebAuthn-Session': session_token },
    body: JSON.stringify(body),
  })
  if (!finishRes.ok) throw new Error(await errMsg(finishRes, 'Could not register this passkey'))
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function loginWithPasskey(): Promise<any> {
  const beginRes = await fetch('/api/login/passkey/begin', { method: 'POST' })
  if (!beginRes.ok) throw new Error(await errMsg(beginRes, 'Could not start passkey login'))
  const { options, session_token } = await beginRes.json()
  const pk = options.publicKey
  pk.challenge = b64urlToBuf(pk.challenge)
  if (pk.allowCredentials) pk.allowCredentials = pk.allowCredentials.map((c: any) => ({ ...c, id: b64urlToBuf(c.id) }))

  const cred = (await navigator.credentials.get({ publicKey: pk })) as PublicKeyCredential
  const resp = cred.response as AuthenticatorAssertionResponse
  const body = {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      authenticatorData: bufToB64url(resp.authenticatorData),
      clientDataJSON: bufToB64url(resp.clientDataJSON),
      signature: bufToB64url(resp.signature),
      userHandle: resp.userHandle ? bufToB64url(resp.userHandle) : undefined,
    },
  }
  const finishRes = await fetch('/api/login/passkey/finish', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-WebAuthn-Session': session_token },
    body: JSON.stringify(body),
  })
  if (!finishRes.ok) throw new Error(await errMsg(finishRes, 'Passkey login failed'))
  return finishRes.json()
}
