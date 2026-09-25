package secrets

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// loadSecretsFile reads the secrets file, decrypting it with sops when it
// carries sops metadata. Shelling out keeps age/KMS key discovery in sops'
// hands, so the host's existing key setup works unchanged.
func loadSecretsFile(path string) (map[string]Set, error) {
	raw, err := readSecretFile(path)
	if err != nil {
		return nil, err
	}
	if !isSOPSEncrypted(raw) {
		return parseSecretsYAML([]byte(raw))
	}

	cmd := exec.Command("sops", "-d", path)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sops -d %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return parseSecretsYAML(stdout.Bytes())
}

func isSOPSEncrypted(body string) bool {
	return strings.Contains(body, "\nsops:") || strings.HasPrefix(body, "sops:")
}
