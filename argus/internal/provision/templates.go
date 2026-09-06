package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// The class templates live inside the Go module so they can be embedded and imported at startup
// (go:embed can't reach outside the module, so this is the canonical home — DESIGN §5 points here).
// Each file is a self-contained Zabbix 7.0 export document imported via configuration.import.
//
//go:embed templates/*.yaml
var templateFS embed.FS

// metaTemplateVersion tracks the hash of the imported template set, so reconcile re-imports only when
// a template file actually changes.
const metaTemplateVersion = "provision_template_version"

type templateDoc struct {
	name    string
	content string
}

// loadTemplates returns the embedded template documents sorted by name, plus a content hash over all
// of them (a change to any file changes the hash and triggers a re-import).
func loadTemplates() ([]templateDoc, string, error) {
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		return nil, "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	h := sha256.New()
	docs := make([]templateDoc, 0, len(names))
	for _, n := range names {
		b, err := templateFS.ReadFile("templates/" + n)
		if err != nil {
			return nil, "", err
		}
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(b)
		docs = append(docs, templateDoc{name: n, content: string(b)})
	}
	return docs, hex.EncodeToString(h.Sum(nil)), nil
}

// Reconcile imports the embedded class templates into Zabbix when they differ from what was last
// imported (tracked by a hash in app_meta). Idempotent and safe on every startup. A Zabbix that is
// unreachable or has no token yet is a soft skip (logged, retried next boot) rather than a fatal
// startup error — the app still serves its read/curate views without provisioning.
func Reconcile(ctx context.Context, zbx *zabbix.Client, st *store.Store, logger *slog.Logger) error {
	if !zbx.Authenticated() {
		logger.Info("provision: skipping template import until a Zabbix API token is configured")
		return nil
	}
	docs, want, err := loadTemplates()
	if err != nil {
		return fmt.Errorf("provision: read embedded templates: %w", err)
	}
	if len(docs) == 0 {
		return nil
	}
	if have, ok, _ := st.MetaGet(ctx, metaTemplateVersion); ok && have == want {
		return nil // already up to date
	}
	for _, d := range docs {
		if err := zbx.ImportConfiguration(ctx, "yaml", d.content); err != nil {
			return fmt.Errorf("provision: import %s: %w", d.name, err)
		}
	}
	if err := st.MetaSet(ctx, metaTemplateVersion, want); err != nil {
		return fmt.Errorf("provision: record template version: %w", err)
	}
	logger.Info("provision: imported class templates", "count", len(docs), "version", want[:12])
	return nil
}
