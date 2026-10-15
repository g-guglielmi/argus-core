// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package mfa

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// MatchStep names the step a code belongs to, not the server's current one: a code made for
// step S is still valid one step later (the skew Validate allows), and must still be reported
// as S, so a claim on S refuses it then too.
func TestMatchStepNamesTheCodesOwnStep(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Argus", AccountName: "t@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_800_000_000, 5)
	code, err := totp.GenerateCode(key.Secret(), at)
	if err != nil {
		t.Fatal(err)
	}
	want := at.Unix() / 30
	for _, later := range []time.Duration{0, 30 * time.Second} {
		step, ok := MatchStep(code, key.Secret(), at.Add(later))
		if !ok || step != want {
			t.Fatalf("at +%s: step %d ok %v, want %d", later, step, ok, want)
		}
	}
	if _, ok := MatchStep(code, key.Secret(), at.Add(60*time.Second)); ok {
		t.Fatal("a code two steps old still matched")
	}
	if _, ok := MatchStep("000000", key.Secret(), at); ok {
		t.Fatal("a wrong code matched")
	}
}
