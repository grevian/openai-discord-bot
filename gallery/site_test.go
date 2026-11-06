package gallery

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedConfig(t *testing.T) {
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.MaxItems != 48 {
		t.Errorf("config = %+v, want enabled with 48 items", c)
	}
	if c.Eligible("") {
		t.Error("DMs must never be eligible")
	}
	if !c.Eligible("some-guild") {
		t.Error("guilds are included by default")
	}
}

func TestParseConfig(t *testing.T) {
	c, err := parseConfig([]byte(`{"enabled":true,"max_items":10,"excluded_guilds":["a","b"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Eligible("a") || c.Eligible("b") || !c.Eligible("c") {
		t.Errorf("exclusion list not honoured: %+v", c)
	}
	for _, bad := range []string{`{`, `{"max_items":-1}`} {
		if _, err := parseConfig([]byte(bad)); err == nil {
			t.Errorf("parseConfig(%s) succeeded", bad)
		}
	}
}

func TestPublishSiteUploadsPageWithTypes(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(defaultCfg, store)
	if err := g.PublishSite(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"index.html":          "text/html; charset=utf-8",
		"home.css":            "text/css; charset=utf-8",
		"gallery/index.html":  "text/html; charset=utf-8",
		"gallery/gallery.js":  "text/javascript; charset=utf-8",
		"gallery/gallery.css": "text/css; charset=utf-8",
		"gallery/help.html":   "text/html; charset=utf-8",
		"help/index.html":     "text/html; charset=utf-8",
	}
	for key, ct := range want {
		o, ok := store.objects[key]
		if !ok {
			t.Errorf("%s not uploaded", key)
			continue
		}
		if o.opts.ContentType != ct {
			t.Errorf("%s Content-Type = %q, want %q", key, o.opts.ContentType, ct)
		}
		if o.opts.CacheControl == "" {
			t.Errorf("%s has no Cache-Control", key)
		}
	}
	if len(store.objects) != len(want) {
		t.Errorf("uploaded %d objects, want %d", len(store.objects), len(want))
	}
}

func TestUploadSiteMountsAndAliases(t *testing.T) {
	fsys := fstest.MapFS{
		"site/page.html":  {Data: []byte("page")},
		"site/skip.txt":   {Data: []byte("not served")},
		"home/index.html": {Data: []byte("home")},
	}
	store := newMemStore()
	g := newTestGallery(defaultCfg, store)
	mounts := []siteMount{{dir: "site", prefix: "gallery/"}, {dir: "home", prefix: ""}}
	aliases := map[string]string{"gallery/page.html": "p/index.html", "gallery/missing.html": "m/index.html"}
	if err := g.uploadSite(context.Background(), fsys, mounts, aliases); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"gallery/page.html", "p/index.html", "index.html"} {
		if _, ok := store.objects[key]; !ok {
			t.Errorf("%s not uploaded", key)
		}
	}
	if string(store.objects["p/index.html"].body) != "page" || store.objects["p/index.html"].opts.ContentType != "text/html; charset=utf-8" {
		t.Errorf("alias is not a copy of its source: %+v", store.objects["p/index.html"])
	}
	if len(store.objects) != 3 {
		t.Errorf("uploaded %d objects, want 3: unknown types and aliases of missing pages are skipped", len(store.objects))
	}
}

func TestHomepageLinksAndPolicy(t *testing.T) {
	b, err := siteFS.ReadFile("home/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	for _, link := range []string{`href="/gallery/"`, `href="/help/"`, `href="https://github.com/grevian/openai-discord-bot"`} {
		if !strings.Contains(html, link) {
			t.Errorf("homepage does not link %s", link)
		}
	}
	if strings.Contains(html, "index.html") {
		t.Error("homepage links must be clean URLs")
	}
	if !strings.Contains(html, "Content-Security-Policy") || strings.Contains(html, "<script") {
		t.Error("homepage needs a CSP and no script")
	}
}

// The CloudFront Function that gives the pages clean URLs, under node.
func TestCleanURLsFunction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "../terraform/clean-urls.test.js").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("clean-urls test failed: %v\n%s", err, out)
	}
}

func TestPublishSiteSkippedWhenDisabled(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(Config{Enabled: false}, store)
	if err := g.PublishSite(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.putCount() != 0 {
		t.Error("a disabled gallery uploaded its page")
	}
}

func readSite(t *testing.T, name string) string {
	t.Helper()
	b, err := siteFS.ReadFile("site/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The page must never build markup from prompt text.
func TestPageNeverUsesHTMLSinks(t *testing.T) {
	js := readSite(t, "gallery.js")
	for _, sink := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "setTimeout(\"", "createContextualFragment", "DOMParser"} {
		// Comments may mention the sinks by name: check code lines only.
		for _, line := range strings.Split(js, "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "*") || strings.HasPrefix(code, "//") || strings.HasPrefix(code, "/*") {
				continue
			}
			if strings.Contains(code, sink) {
				t.Errorf("gallery.js uses %q: %s", sink, code)
			}
		}
	}
}

func TestPageIsNoindexWithStrictCSP(t *testing.T) {
	for _, name := range []string{"index.html", "help.html"} {
		t.Run(name, func(t *testing.T) { checkNoindexStrictCSP(t, name) })
	}
}

func checkNoindexStrictCSP(t *testing.T, name string) {
	t.Helper()
	html := readSite(t, name)
	if !regexp.MustCompile(`<meta name="robots" content="[^"]*noindex`).MatchString(html) {
		t.Error("index.html must be noindex until moderation exists")
	}
	if !strings.Contains(html, `Content-Security-Policy`) || !strings.Contains(html, "script-src 'self'") {
		t.Error("index.html must carry a CSP restricting scripts to the page's origin")
	}
	for _, m := range regexp.MustCompile(`<script[^>]*>`).FindAllString(html, -1) {
		if !strings.Contains(m, "src=") {
			t.Errorf("inline script: %s", m)
		}
	}
	if strings.Contains(html, "http://") || strings.Contains(html, "https://") {
		t.Error("index.html must not reference external hosts")
	}
}

// Runs the renderer under node against hostile input.
func TestRendererIsSafeAgainstHostileInput(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	cmd := exec.Command(node, "testdata/render_test.js", "site/gallery.js")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node test failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ok") {
		t.Errorf("unexpected output: %s", out)
	}
}
