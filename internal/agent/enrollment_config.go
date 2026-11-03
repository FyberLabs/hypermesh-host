package agent

import (
	"fmt"
	"strings"
)

// missingEnrollmentConfig only reports configuration keys, never their values.
// A saved identity (including the documented recovery override) needs no token.
func missingEnrollmentConfig(cfg Config, st State) error {
	var missing []string
	if strings.TrimSpace(cfg.TenantID) == "" {
		missing = append(missing, "HYPERMESH_TENANT_ID")
	}
	secret := st.DeviceSecret
	if secret == "" {
		secret = cfg.DeviceSecret
	}
	haveIdentity := strings.TrimSpace(st.DeviceID) != "" && strings.TrimSpace(secret) != ""
	if !haveIdentity && strings.TrimSpace(cfg.EnrollToken) == "" {
		missing = append(missing, "HYPERMESH_ENROLL_TOKEN")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("hypermesh: missing required configuration: %s; complete owner registration, or set the missing keys for secondary token enrollment before restarting the agent", strings.Join(missing, ", "))
}
