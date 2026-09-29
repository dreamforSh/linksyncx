//go:build integration

// Package tlsfingerprint provides TLS fingerprint simulation for HTTP clients.
//
// Integration tests for verifying TLS fingerprint correctness.
// These tests make actual network requests to external services and should be run manually.
//
// Run with: go test -v -tags=integration ./internal/pkg/tlsfingerprint/...
package tlsfingerprint

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// skipIfExternalServiceUnavailable checks if the external service is available.
// If not, it skips the test instead of failing.
func skipIfExternalServiceUnavailable(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		// Check for common network/TLS errors that indicate external service issues
		errStr := err.Error()
		if strings.Contains(errStr, "certificate has expired") ||
			strings.Contains(errStr, "certificate is not yet valid") ||
			strings.Contains(errStr, "connection refused") ||
			strings.Contains(errStr, "connection reset") ||
			strings.Contains(errStr, "broken pipe") ||
			strings.Contains(errStr, "EOF") ||
			strings.Contains(errStr, "no such host") ||
			strings.Contains(errStr, "network is unreachable") ||
			strings.Contains(errStr, "timeout") ||
			strings.Contains(errStr, "deadline exceeded") {
			t.Skipf("skipping test: external service unavailable: %v", err)
		}
		t.Fatalf("failed to get fingerprint: %v", err)
	}
}

// TestJA3Fingerprint verifies the JA3/JA4 fingerprint matches expected value.
// This test uses tls.peet.ws to verify the fingerprint.
// Expected JA3 hash: 1523504b38f0fae0d881d4b6554aac1b (Claude Code 2.1.280 / Bun, SNI connection)
// Expected JA4: t13d1713h1_5b57614c22b0_6a3d802a7139
func TestJA3Fingerprint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	profile := &Profile{
		Name:         "Default Profile Test",
		EnableGREASE: false,
	}
	dialer := NewDialer(profile, nil)

	client := &http.Client{
		Transport: &http.Transport{
			DialTLSContext: dialer.DialTLSContext,
		},
		Timeout: 30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://tls.peet.ws/api/all", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", "Claude Code/2.0.0 Node.js/24.3.0")

	resp, err := client.Do(req)
	skipIfExternalServiceUnavailable(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	skipIfExternalServiceUnavailable(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Skipf("skipping test: external service unhealthy: status %d", resp.StatusCode)
	}

	var fpResp FingerprintResponse
	if err := json.Unmarshal(body, &fpResp); err != nil {
		t.Logf("Response body: %s", string(body))
		t.Skipf("skipping test: external service returned unparsable response: %v", err)
	}

	t.Logf("JA3: %s", fpResp.TLS.JA3)
	t.Logf("JA3 Hash: %s", fpResp.TLS.JA3Hash)
	t.Logf("JA4: %s", fpResp.TLS.JA4)

	expectedJA3Hash := "1523504b38f0fae0d881d4b6554aac1b"
	if fpResp.TLS.JA3Hash == expectedJA3Hash {
		t.Logf("✓ JA3 hash matches: %s", expectedJA3Hash)
	} else {
		t.Errorf("✗ JA3 hash mismatch: got %s, expected %s", fpResp.TLS.JA3Hash, expectedJA3Hash)
	}

	expectedJA4CipherHash := "_5b57614c22b0_"
	if strings.Contains(fpResp.TLS.JA4, expectedJA4CipherHash) {
		t.Logf("✓ JA4 cipher hash matches: %s", expectedJA4CipherHash)
	} else {
		t.Errorf("✗ JA4 cipher hash mismatch: got %s, expected containing %s", fpResp.TLS.JA4, expectedJA4CipherHash)
	}
}

// TestAllProfiles tests multiple TLS fingerprint profiles against tls.peet.ws.
// Run with: go test -v -tags=integration -run TestAllProfiles ./internal/pkg/tlsfingerprint/...
func TestAllProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Define all profiles to test with their expected fingerprints
	// These profiles are from config.yaml gateway.tls_fingerprint.profiles
	profiles := []TestProfileExpectation{
		{
			// Default profile (Claude Code 2.1.280 / Bun)
			Profile: &Profile{
				Name:         "default_claude_code_2_1_280",
				EnableGREASE: false,
			},
			JA4CipherHash: "5b57614c22b0",
		},
		{
			// Linux x64 Node.js v22.17.1 (explicit profile with v22 extensions)
			Profile:       node22171TestProfile(),
			JA4CipherHash: "a33745022dd6",
		},
	}

	for _, tc := range profiles {
		tc := tc // capture range variable
		t.Run(tc.Profile.Name, func(t *testing.T) {
			fp := fetchFingerprint(t, tc.Profile)
			if fp == nil {
				return // fetchFingerprint already called t.Fatal
			}

			t.Logf("Profile: %s", tc.Profile.Name)
			t.Logf("  JA3:           %s", fp.JA3)
			t.Logf("  JA3 Hash:      %s", fp.JA3Hash)
			t.Logf("  JA4:           %s", fp.JA4)
			t.Logf("  PeetPrint:     %s", fp.PeetPrint)
			t.Logf("  PeetPrintHash: %s", fp.PeetPrintHash)

			// Verify expectations
			if tc.ExpectedJA3 != "" {
				if fp.JA3Hash == tc.ExpectedJA3 {
					t.Logf("  ✓ JA3 hash matches: %s", tc.ExpectedJA3)
				} else {
					t.Errorf("  ✗ JA3 hash mismatch: got %s, expected %s", fp.JA3Hash, tc.ExpectedJA3)
				}
			}

			if tc.ExpectedJA4 != "" {
				if fp.JA4 == tc.ExpectedJA4 {
					t.Logf("  ✓ JA4 matches: %s", tc.ExpectedJA4)
				} else {
					t.Errorf("  ✗ JA4 mismatch: got %s, expected %s", fp.JA4, tc.ExpectedJA4)
				}
			}

			// Check JA4 cipher hash (stable middle part)
			// JA4 format: prefix_cipherHash_extHash
			if tc.JA4CipherHash != "" {
				if strings.Contains(fp.JA4, "_"+tc.JA4CipherHash+"_") {
					t.Logf("  ✓ JA4 cipher hash matches: %s", tc.JA4CipherHash)
				} else {
					t.Errorf("  ✗ JA4 cipher hash mismatch: got %s, expected cipher hash %s", fp.JA4, tc.JA4CipherHash)
				}
			}
		})
	}
}

// fetchFingerprint makes a request to tls.peet.ws and returns the TLS fingerprint info.
func fetchFingerprint(t *testing.T, profile *Profile) *TLSInfo {
	t.Helper()

	dialer := NewDialer(profile, nil)
	client := &http.Client{
		Transport: &http.Transport{
			DialTLSContext: dialer.DialTLSContext,
		},
		Timeout: 30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://tls.peet.ws/api/all", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
		return nil
	}
	req.Header.Set("User-Agent", "Claude Code/2.0.0 Node.js/20.0.0")

	resp, err := client.Do(req)
	skipIfExternalServiceUnavailable(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	skipIfExternalServiceUnavailable(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Skipf("skipping test: external service unhealthy: status %d", resp.StatusCode)
	}

	var fpResp FingerprintResponse
	if err := json.Unmarshal(body, &fpResp); err != nil {
		t.Logf("Response body: %s", string(body))
		t.Skipf("skipping test: external service returned unparsable response: %v", err)
	}

	return &fpResp.TLS
}
