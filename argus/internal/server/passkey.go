// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"argus/internal/auth"
	"argus/internal/store"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const webauthnSessionTTL = 5 * time.Minute

// waUser adapts a stored user (plus its credentials) to the webauthn.User interface.
type waUser struct {
	handle []byte
	u      *store.User
	creds  []webauthn.Credential
}

func (x *waUser) WebAuthnID() []byte   { return x.handle }
func (x *waUser) WebAuthnName() string { return x.u.Email }
func (x *waUser) WebAuthnDisplayName() string {
	if n := strings.TrimSpace(x.u.Name + " " + x.u.Surname); n != "" {
		return n
	}
	return x.u.Email
}
func (x *waUser) WebAuthnCredentials() []webauthn.Credential { return x.creds }

// waUserFor loads the stable handle and decoded credentials for a user.
func (s *Server) waUserFor(ctx context.Context, u *store.User) (*waUser, error) {
	handle, err := s.st.EnsureWebAuthnHandle(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	blobs, err := s.st.PasskeyCredentials(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	creds := make([]webauthn.Credential, 0, len(blobs))
	for _, b := range blobs {
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(b), &c); err == nil {
			creds = append(creds, c)
		}
	}
	return &waUser{handle: handle, u: u, creds: creds}, nil
}

// storeWASession persists ceremony data and returns the raw token the client echoes back.
func (s *Server) storeWASession(ctx context.Context, userID *int64, sd *webauthn.SessionData) (string, error) {
	raw, id, err := auth.NewSessionToken()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(sd)
	if err != nil {
		return "", err
	}
	if err := s.st.SaveWebAuthnSession(ctx, id, userID, string(data), time.Now().Add(webauthnSessionTTL)); err != nil {
		return "", err
	}
	return raw, nil
}

// loadWASession restores ceremony data referenced by the X-WebAuthn-Session header.
func (s *Server) loadWASession(ctx context.Context, r *http.Request) (userID *int64, sd webauthn.SessionData, id string, ok bool) {
	id = auth.HashToken(r.Header.Get("X-WebAuthn-Session"))
	uid, data, err := s.st.WebAuthnSession(ctx, id)
	if err != nil {
		return nil, webauthn.SessionData{}, "", false
	}
	if err := json.Unmarshal([]byte(data), &sd); err != nil {
		return nil, webauthn.SessionData{}, "", false
	}
	return uid, sd, id, true
}

// --- registration (authenticated) ---

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if s.wa == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "passkeys are not enabled on this server"})
		return
	}
	ctx := r.Context()
	caller, _ := auth.UserFrom(ctx)
	u, err := s.st.UserByID(ctx, caller.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	wu, err := s.waUserFor(ctx, u)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	sel := protocol.AuthenticatorSelection{
		ResidentKey:      protocol.ResidentKeyRequirementRequired,
		UserVerification: protocol.VerificationPreferred,
	}
	options, sessionData, err := s.wa.BeginRegistration(wu, webauthn.WithAuthenticatorSelection(sel))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not start registration"})
		return
	}
	token, err := s.storeWASession(ctx, &u.ID, sessionData)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"options": options, "session_token": token})
}

func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if s.wa == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "passkeys are not enabled on this server"})
		return
	}
	ctx := r.Context()
	caller, _ := auth.UserFrom(ctx)
	uid, sd, sid, ok := s.loadWASession(ctx, r)
	if !ok || uid == nil || *uid != caller.ID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no active registration; start again"})
		return
	}
	u, err := s.st.UserByID(ctx, caller.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	wu, err := s.waUserFor(ctx, u)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	cred, err := s.wa.FinishRegistration(wu, sd, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not register this passkey"})
		return
	}
	_ = s.st.DeleteWebAuthnSession(ctx, sid)

	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "Passkey"
	}
	blob, err := json.Marshal(cred)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.st.AddPasskey(ctx, cred.ID, u.ID, name, string(blob)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- login (public, discoverable) ---

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if s.wa == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "passkeys are not enabled on this server"})
		return
	}
	ctx := r.Context()
	options, sessionData, err := s.wa.BeginDiscoverableLogin()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not start passkey login"})
		return
	}
	token, err := s.storeWASession(ctx, nil, sessionData)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"options": options, "session_token": token})
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if s.wa == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "passkeys are not enabled on this server"})
		return
	}
	ctx := r.Context()
	_, sd, sid, ok := s.loadWASession(ctx, r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "this sign-in has expired; please start again"})
		return
	}

	var loggedIn *store.User
	handler := func(_, userHandle []byte) (webauthn.User, error) {
		u, err := s.st.UserByWebAuthnHandle(ctx, userHandle)
		if err != nil {
			return nil, err
		}
		wu, err := s.waUserFor(ctx, u)
		if err != nil {
			return nil, err
		}
		loggedIn = u
		return wu, nil
	}

	cred, err := s.wa.FinishDiscoverableLogin(handler, sd, r)
	if err != nil || loggedIn == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "passkey login failed"})
		return
	}
	_ = s.st.DeleteWebAuthnSession(ctx, sid)
	if blob, err := json.Marshal(cred); err == nil {
		_ = s.st.UpdatePasskeyCredential(ctx, cred.ID, string(blob))
	}
	s.issueSession(w, r, loggedIn)
}

// --- management ---

type passkeyView struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Created  string  `json:"created"`
	LastUsed *string `json:"last_used"`
}

func (s *Server) handleListPasskeys(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.UserFrom(r.Context())
	pks, err := s.st.ListPasskeys(r.Context(), caller.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	out := make([]passkeyView, 0, len(pks))
	for _, p := range pks {
		v := passkeyView{
			ID:      base64.RawURLEncoding.EncodeToString(p.ID),
			Name:    p.Name,
			Created: p.CreatedAt.UTC().Format(time.RFC3339),
		}
		if p.LastUsedAt != nil {
			t := p.LastUsedAt.UTC().Format(time.RFC3339)
			v.LastUsed = &t
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeletePasskey(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.UserFrom(r.Context())
	id, err := base64.RawURLEncoding.DecodeString(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := s.st.DeletePasskey(r.Context(), id, caller.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminResetPasskeys(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.st.UserByID(r.Context(), id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	if err := s.st.DeleteAllPasskeys(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
