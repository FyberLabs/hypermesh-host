package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/FyberLabs/hypermesh-host/internal/api"
)

// A sidecar survives index reconstruction and pending-pin expiry. Write and
// fsync it BEFORE fetching private bytes, so a crash cannot admit an unmarked
// private file into the public cache. It contains no URL or credential.
type privateCacheBinding struct {
	Tenant              string `json:"tenant"`
	Revision            string `json:"revision"`
	Backend             string `json:"backend"`
	Runtime             string `json:"runtime"`
	Image               string `json:"image"`
	ConfigurationSHA256 string `json:"configuration_sha256"`
	SizeBytes           int64  `json:"size_bytes"`
}

func (a *Agent) privateBinding(job api.Job) (privateCacheBinding, error) {
	revision, err := privateRevision(job.SourceRef)
	hash, configErr := modelConfigurationHash(job.ModelConfiguration)
	if err != nil || configErr != nil || hash != job.ModelConfigurationSHA256 {
		return privateCacheBinding{}, fmt.Errorf("private cache runtime binding invalid")
	}
	return privateCacheBinding{Tenant: strings.ToLower(a.cfg.TenantID), Revision: revision, Backend: job.Backend,
		Runtime: job.Runtime, Image: job.ImageDigest, ConfigurationSHA256: hash, SizeBytes: job.SizeBytes}, nil
}

func readPrivateBindings(path string) ([]privateCacheBinding, error) {
	file, err := os.Open(path + ".private.json")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("private cache provenance unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	var bindings []privateCacheBinding
	if err != nil || len(data) > 65536 || json.Unmarshal(data, &bindings) != nil || len(bindings) == 0 || len(bindings) > 64 {
		return nil, fmt.Errorf("private cache provenance invalid")
	}
	return bindings, nil
}

func (a *Agent) rememberPrivateCache(job api.Job, path string) error {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	binding, err := a.privateBinding(job)
	if err != nil {
		return err
	}
	bindings, err := readPrivateBindings(path)
	if err != nil {
		return err
	}
	for _, existing := range bindings {
		if existing == binding {
			return nil
		}
	}
	if len(bindings) >= 64 {
		return fmt.Errorf("private cache scope limit reached")
	}
	bindings = append(bindings, binding)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("private cache provenance unavailable")
	}
	data, err := json.Marshal(bindings)
	if err != nil {
		return fmt.Errorf("private cache provenance invalid")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".private-provenance-*")
	if err != nil {
		return fmt.Errorf("private cache provenance unavailable")
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if err == nil {
		err = tmp.Close()
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path+".private.json")
	}
	if err != nil {
		return fmt.Errorf("private cache provenance unavailable")
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("private cache provenance unavailable")
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return fmt.Errorf("private cache provenance unavailable")
	}
	return nil
}

func (a *Agent) checkPrivateCacheBinding(job api.Job, path string) (bool, error) {
	bindings, err := readPrivateBindings(path)
	if err != nil {
		return false, err
	}
	if len(bindings) == 0 {
		if strings.HasPrefix(job.SourceRef, privateModelPrefix) {
			return false, fmt.Errorf("private cache provenance required")
		}
		return false, nil
	}
	wanted, err := a.privateBinding(job)
	if err != nil {
		return false, fmt.Errorf("private cache requires a matching private model reference")
	}
	for _, binding := range bindings {
		if binding == wanted {
			return true, nil
		}
	}
	return false, fmt.Errorf("private cache scope differs")
}

func (a *Agent) authorizePrivateCacheFile(ctx context.Context, job api.Job, path string) error {
	private, err := a.checkPrivateCacheBinding(job, path)
	if err != nil {
		return err
	}
	if private {
		if _, err := a.authorizedPrivateGrant(ctx, job); err != nil {
			return fmt.Errorf("private model grant unavailable")
		}
	}
	return nil
}
