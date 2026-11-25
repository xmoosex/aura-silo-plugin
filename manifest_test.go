package main

import (
	"testing"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

// The manifest has what Silo's catalogs need, as index.json is built from it
func TestManifestIsReadyForACatalog(t *testing.T) {
	m, err := manifest.Load(manifestJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.ValidateCatalogPresentation(m, "https://github.com/xmoosex/aura-silo-plugin"); err != nil {
		t.Fatal(err)
	}
}
