package gallery

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
)

//go:embed config.json
var embeddedConfig []byte

// siteFS is the static page that is uploaded to the bucket.
//
//go:embed site
var siteFS embed.FS

const siteCacheControl = "public, max-age=300"

// LoadConfig returns the checked-in policy from config.json.
func LoadConfig() (Config, error) {
	return parseConfig(embeddedConfig)
}

func parseConfig(raw []byte) (Config, error) {
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("parse gallery config: %w", err)
	}
	if c.MaxItems < 0 {
		return Config{}, errors.New("gallery config: max_items must not be negative")
	}
	return c, nil
}

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
}

// PublishSite uploads the static page (index.html, gallery.js, gallery.css) to
// gallery/ in the bucket. The bot calls it on start so that deploying the bot
// also deploys the page. Every file is attempted; the errors are joined.
func (g *Gallery) PublishSite(ctx context.Context) error {
	if !g.cfg.Enabled {
		return nil
	}
	entries, err := fs.ReadDir(siteFS, "site")
	if err != nil {
		return fmt.Errorf("read embedded site: %w", err)
	}
	var errs []error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := fs.ReadFile(siteFS, path.Join("site", e.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ct, ok := contentTypes[path.Ext(e.Name())]
		if !ok {
			continue
		}
		if err := g.store.Put(ctx, Prefix+e.Name(), body, PutOptions{ContentType: ct, CacheControl: siteCacheControl}); err != nil {
			errs = append(errs, fmt.Errorf("upload %s: %w", e.Name(), err))
		}
	}
	return errors.Join(errs...)
}
