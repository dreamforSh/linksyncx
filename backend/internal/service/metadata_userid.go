package service

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/tidwall/sjson"
)

// NewMetadataFormatMinVersion is the minimum Claude Code version that uses
// JSON-formatted metadata.user_id instead of the legacy concatenated string.
const NewMetadataFormatMinVersion = "2.1.78"

// ParsedUserID represents the components extracted from a metadata.user_id value.
type ParsedUserID struct {
	DeviceID    string // 64-char hex (or arbitrary client id)
	AccountUUID string // may be empty
	SessionID   string // UUID
	// ParentSessionID is the parent session of a spawned/team session
	// (Claude Code 2.1.287 ije: parent_session_id); empty when absent.
	ParentSessionID string
	IsNewFormat     bool // true if the original was JSON format
}

// legacyUserIDRegex matches the legacy user_id format:
//
//	user_{64hex}_account_{optional_uuid}_session_{uuid}
var legacyUserIDRegex = regexp.MustCompile(`^user_([a-fA-F0-9]{64})_account_([a-fA-F0-9-]*)_session_([a-fA-F0-9-]{36})$`)

// jsonUserID is the JSON structure for the new metadata.user_id format.
type jsonUserID struct {
	DeviceID        string `json:"device_id"`
	AccountUUID     string `json:"account_uuid"`
	SessionID       string `json:"session_id"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
}

// ParseMetadataUserID parses a metadata.user_id string in either format.
// Returns nil if the input cannot be parsed.
func ParseMetadataUserID(raw string) *ParsedUserID {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	// Try JSON format first (starts with '{')
	if raw[0] == '{' {
		var j jsonUserID
		if err := json.Unmarshal([]byte(raw), &j); err != nil {
			return nil
		}
		if j.DeviceID == "" || j.SessionID == "" {
			return nil
		}
		return &ParsedUserID{
			DeviceID:        j.DeviceID,
			AccountUUID:     j.AccountUUID,
			SessionID:       j.SessionID,
			ParentSessionID: j.ParentSessionID,
			IsNewFormat:     true,
		}
	}

	// Try legacy format
	matches := legacyUserIDRegex.FindStringSubmatch(raw)
	if matches == nil {
		return nil
	}
	return &ParsedUserID{
		DeviceID:    matches[1],
		AccountUUID: matches[2],
		SessionID:   matches[3],
		IsNewFormat: false,
	}
}

// FormatMetadataUserID builds a metadata.user_id string in the format
// appropriate for the given CLI version. Components are the rewritten values
// (not necessarily the originals).
func FormatMetadataUserID(deviceID, accountUUID, sessionID, uaVersion string) string {
	if IsNewMetadataFormatVersion(uaVersion) {
		b, _ := json.Marshal(jsonUserID{
			DeviceID:    deviceID,
			AccountUUID: accountUUID,
			SessionID:   sessionID,
		})
		return string(b)
	}
	// Legacy format
	return "user_" + deviceID + "_account_" + accountUUID + "_session_" + sessionID
}

// rewriteJSONMetadataUserID replaces the identity fields of a JSON-format
// metadata.user_id in place. Key order and any other fields the client sent
// (ti, tk, CLAUDE_CODE_EXTRA_METADATA entries) are kept byte-for-byte; real
// Claude Code builds the object as {...extra, ti?, device_id, account_uuid,
// session_id, parent_session_id?, tk?}. parentSessionID replaces an existing
// parent_session_id; dropParent removes it instead. Returns ok=false when raw
// is not a JSON object, so callers can fall back to FormatMetadataUserID.
func rewriteJSONMetadataUserID(raw, deviceID, accountUUID, sessionID, parentSessionID string, dropParent bool) (string, bool) {
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") || !json.Valid([]byte(raw)) {
		return "", false
	}
	out := raw
	var err error
	for _, field := range [...]struct{ key, value string }{
		{"device_id", deviceID},
		{"account_uuid", accountUUID},
		{"session_id", sessionID},
	} {
		if out, err = sjson.Set(out, field.key, field.value); err != nil {
			return "", false
		}
	}
	switch {
	case dropParent:
		if out, err = sjson.Delete(out, "parent_session_id"); err != nil {
			return "", false
		}
	case parentSessionID != "":
		if out, err = sjson.Set(out, "parent_session_id", parentSessionID); err != nil {
			return "", false
		}
	}
	return out, true
}

// IsNewMetadataFormatVersion returns true if the given CLI version uses the
// new JSON metadata.user_id format (>= 2.1.78).
func IsNewMetadataFormatVersion(version string) bool {
	if version == "" {
		return false
	}
	return CompareVersions(version, NewMetadataFormatMinVersion) >= 0
}

// ExtractCLIVersion extracts the Claude Code version from a User-Agent string.
// Returns "" if the UA doesn't match the expected pattern.
func ExtractCLIVersion(ua string) string {
	matches := claudeCodeUAVersionPattern.FindStringSubmatch(ua)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}
