package tlsfingerprint

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	utls "github.com/refraction-networking/utls"
)

// ValidateProfile checks the effective wire values, including defaults.
func ValidateProfile(profile *Profile) error {
	spec := buildClientHelloSpecFromProfile(profile)
	groups := map[utls.CurveID]bool{}
	var shares []utls.KeyShare
	for _, extension := range spec.Extensions {
		switch ext := extension.(type) {
		case *utls.SupportedCurvesExtension:
			for _, group := range ext.Curves {
				groups[group] = true
			}
		case *utls.KeyShareExtension:
			shares = ext.KeyShares
		}
	}
	for _, share := range shares {
		if !isGREASEValue(uint16(share.Group)) && !groups[share.Group] {
			return fmt.Errorf("TLS key share %d is absent from supported groups", share.Group)
		}
	}
	return nil
}

// ProfileCacheKey excludes display names; a wire configuration change gets a new pool.
func ProfileCacheKey(profile *Profile) string {
	var value Profile
	if profile != nil {
		value = *profile
	}
	value.Name = ""
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}
