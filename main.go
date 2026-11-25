// The aura Silo plugin gives Silo the MediUX artwork from an aura server
package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
	"github.com/hashicorp/go-hclog"
)

//go:embed manifest.json
var manifestJSON []byte

// Set when building a release
var version string

// loadManifest reads the manifest. Silo compares it with the installed one, so it needs this binary's checksum
func loadManifest() (*pluginv1.PluginManifest, error) {
	m, err := manifest.Load(manifestJSON)
	if err != nil {
		return nil, fmt.Errorf("loading the manifest: %w", err)
	}
	if version != "" {
		m.Version = version
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	m.Checksum = hex.EncodeToString(sum[:])
	return m, nil
}

func main() {
	logger := hclog.New(&hclog.LoggerOptions{Name: "aura"})
	m, err := loadManifest()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s := &server{manifest: m, aura: newAuraClient(), logger: logger}
	runtime.Serve(runtime.ServeConfig{
		Logger:  logger,
		Servers: runtime.CapabilityServers{Runtime: s, MetadataProvider: s, ImageResolver: s},
	})
}
