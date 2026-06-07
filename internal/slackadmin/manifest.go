package slackadmin

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// callbackPlaceholder is the token the manifest uses for the public base URL.
// It matches the Makefile slack-sync target so the file works either way.
const callbackPlaceholder = "${SLACK_CALLBACK_URL}"

// LoadManifest reads the YAML manifest, substitutes the callback base URL, and
// returns it as a generic map ready to be marshaled into the API payload.
// yaml.v3 decodes mappings into map[string]any, so the result round-trips
// cleanly through encoding/json (unlike yaml.v2's map[interface{}]interface{}).
func LoadManifest(path, callbackURL string) (map[string]any, error) {
	callbackURL = strings.TrimRight(callbackURL, "/")
	if callbackURL == "" {
		return nil, fmt.Errorf("callback base URL is required to render %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	substituted := strings.ReplaceAll(string(raw), callbackPlaceholder, callbackURL)
	if strings.Contains(substituted, callbackPlaceholder) {
		return nil, fmt.Errorf("manifest %s still contains %s after substitution", path, callbackPlaceholder)
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(substituted), &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return m, nil
}
