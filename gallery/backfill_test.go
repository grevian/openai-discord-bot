package gallery

import (
	"context"
	"testing"
	"time"

	"github.com/segmentio/ksuid"
)

func ksAt(t time.Time) string {
	k, _ := ksuid.NewRandomWithTime(t)
	return k.String()
}

func TestSelectBackfill(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var keys []string
	for i := 0; i < 5; i++ {
		keys = append(keys, "111/"+ksAt(base.Add(time.Duration(i)*time.Hour)))
	}
	dm := "private-chat/" + ksAt(base.Add(10*time.Hour))
	ex := "222/" + ksAt(base.Add(11*time.Hour))
	amb := "weird/" + ksAt(base.Add(12*time.Hour))
	keys = append(keys, dm, ex, amb, "gallery/latest.json", "gallery/items/x.json", "111/not-a-ksuid", "toplevel")

	cfg := Config{Enabled: true, MaxItems: 3, ExcludedGuilds: []string{"222"}}
	items, st := SelectBackfill(cfg, keys)
	if st.Scanned != len(keys) || st.DM != 1 || st.GuildExcluded != 1 || st.Ambiguous != 1 || st.NotImage != 4 {
		t.Fatalf("stats = %+v", st)
	}
	if len(items) != 3 || items[0].ImageKey != keys[4] || items[2].ImageKey != keys[2] {
		t.Fatalf("items = %+v", items)
	}
	for _, it := range items {
		if it.GuildID != "111" {
			t.Errorf("unexpected guild %q", it.GuildID)
		}
	}
}

func TestPublishBackfill(t *testing.T) {
	store := newMemStore()
	g := New(Config{Enabled: true, MaxItems: 48}, store)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	k1, k2 := "111/"+ksAt(base), "111/"+ksAt(base.Add(time.Hour))
	items := []BackfillItem{
		{Submission: Submission{GuildID: "111", ImageKey: k1, Prompt: "hi <@123> @everyone"}, Created: base},
		{Submission: Submission{GuildID: "111", ImageKey: k2, Kind: KindEdit}, Created: base.Add(time.Hour)},
		{Submission: Submission{GuildID: "", ImageKey: "private-chat/" + ksAt(base)}, Created: base},
	}
	got, err := g.PublishBackfill(context.Background(), items)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	m, _, err := g.loadManifest(context.Background())
	if err != nil || len(m.Items) != 2 || m.Items[0].Key != k2 || m.Items[1].Prompt != "hi someone everyone" {
		t.Fatalf("manifest = %+v, %v", m, err)
	}
	if !m.Items[1].Created.Equal(base) {
		t.Errorf("created = %v", m.Items[1].Created)
	}
	// Idempotent.
	if _, err := g.PublishBackfill(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	m, _, _ = g.loadManifest(context.Background())
	if len(m.Items) != 2 {
		t.Fatalf("duplicated: %+v", m.Items)
	}
}
