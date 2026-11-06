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

// siteFS holds the static pages that are uploaded to the bucket: site/ is the
// gallery (keys under gallery/) and home/ is the homepage (keys at the root).
//
//go:embed site home
var siteFS embed.FS

// siteMount maps a directory of siteFS to a key prefix in the bucket.
type siteMount struct{ dir, prefix string }

var siteMounts = []siteMount{
	{dir: "site", prefix: Prefix},
	{dir: "home", prefix: ""},
}

// siteAliases are extra copies of an uploaded key, so a page also answers at a
// clean URL (/help/ is served by CloudFront as help/index.html) while its old
// URL keeps working. An alias is skipped when its source does not exist.
var siteAliases = map[string]string{
	Prefix + "help.html": "help/index.html",
}

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

// PublishSite uploads the static pages to the bucket: the gallery under
// gallery/ and the homepage at the root (see siteMounts). The bot calls it on
// start so that deploying the bot also deploys the pages. Every file is
// attempted; the errors are joined.
func (g *Gallery) PublishSite(ctx context.Context) error {
	if !g.cfg.Enabled {
		return nil
	}
	return g.uploadSite(ctx, siteFS, siteMounts, siteAliases)
}

func (g *Gallery) uploadSite(ctx context.Context, fsys fs.FS, mounts []siteMount, aliases map[string]string) error {
	var errs []error
	put := func(key string, body []byte, ct string) {
		if err := g.store.Put(ctx, key, body, PutOptions{ContentType: ct, CacheControl: siteCacheControl}); err != nil {
			errs = append(errs, fmt.Errorf("upload %s: %w", key, err))
		}
	}
	uploaded := map[string][]byte{}
	for _, m := range mounts {
		entries, err := fs.ReadDir(fsys, m.dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("read embedded %s: %w", m.dir, err))
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ct, ok := contentTypes[path.Ext(e.Name())]
			if !ok {
				continue
			}
			body, err := fs.ReadFile(fsys, path.Join(m.dir, e.Name()))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			key := m.prefix + e.Name()
			uploaded[key] = body
			put(key, body, ct)
		}
	}
	for src, dst := range aliases {
		if body, ok := uploaded[src]; ok {
			put(dst, body, contentTypes[path.Ext(src)])
		}
	}
	return errors.Join(errs...)
}
