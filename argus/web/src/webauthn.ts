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

// The browser's own WebAuthn errors are terse and point at the spec ("The operation either timed
// out or was not allowed. See: https://www.w3.org/..."). Say what happened in Argus's words.
function friendly(e: unknown, action: 'setup' | 'login'): Error {
  const name = e instanceof DOMException ? e.name : ''
  const what = action === 'setup' ? 'Passkey setup' : 'Passkey sign-in'
  switch (name) {
    case 'NotAllowedError': return new Error(`${what} was cancelled or timed out. Nothing was ${action === 'setup' ? 'added' : 'changed'}; try again when you're ready.`)
    case 'InvalidStateError': return new Error('This authenticator already holds a passkey for this account.')
    case 'SecurityError': return new Error("Passkeys only work when you reach Argus through its HTTPS address (the one configured as the passkey domain).")
    case 'AbortError': return new Error(`${what} was interrupted.`)
    case 'NotSupportedError': return new Error("This browser or authenticator doesn't support the kind of passkey Argus asks for (a discoverable key with user verification).")
  }
  return e instanceof Error && e.message ? e : new Error(`${what} failed.`)
}

async function errMsg(res: Response, fallback: string): Promise<string> {
  const j = await res.json().catch(() => ({}))
  return (j && j.error) || fallback
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function registerPasskey(name: string, password: string): Promise<void> {
  const beginRes = await fetch('/api/me/passkeys/register/begin', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password }) })
  if (!beginRes.ok) throw new Error(await errMsg(beginRes, 'Could not start passkey setup'))
  const { options, session_token } = await beginRes.json()
  const pk = options.publicKey
  pk.challenge = b64urlToBuf(pk.challenge)
  pk.user.id = b64urlToBuf(pk.user.id)
  if (pk.excludeCredentials) pk.excludeCredentials = pk.excludeCredentials.map((c: any) => ({ ...c, id: b64urlToBuf(c.id) }))

  let cred: PublicKeyCredential
  try { cred = (await navigator.credentials.create({ publicKey: pk })) as PublicKeyCredential } catch (e) { throw friendly(e, 'setup') }
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

  let cred: PublicKeyCredential
  try { cred = (await navigator.credentials.get({ publicKey: pk })) as PublicKeyCredential } catch (e) { throw friendly(e, 'login') }
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
