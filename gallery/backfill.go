package gallery

import (
	"sort"
	"strings"
	"time"

	"github.com/segmentio/ksuid"
)

// DMPrefix is the key prefix openai-discord-bot's StoreImage gives images made
// in direct messages (an empty guild ID becomes "private-chat"). Guild images
// live under "<numeric guild id>/".
const DMPrefix = "private-chat"

// BackfillStats counts what SelectBackfill did with the keys it was given.
type BackfillStats struct {
	// Scanned is every key looked at.
	Scanned int
	// NotImage is keys that are not "<group>/<ksuid>" (the gallery's own
	// objects, stray files).
	NotImage int
	// DM is images from direct messages. Never published.
	DM int
	// Ambiguous is images whose group is neither "private-chat" nor a numeric
	// guild ID. They cannot be classified, so they are left out.
	Ambiguous int
	// GuildExcluded is images from guilds on the exclude list.
	GuildExcluded int
}

// ParseImageKey splits a key written by StoreImage into its group and ksuid
// and reports when the image was made (the ksuid's embedded time).
func ParseImageKey(key string) (group, id string, created time.Time, ok bool) {
	i := strings.Index(key, "/")
	if i <= 0 || strings.Contains(key[i+1:], "/") {
		return "", "", time.Time{}, false
	}
	group, id = key[:i], key[i+1:]
	k, err := ksuid.Parse(id)
	if err != nil {
		return "", "", time.Time{}, false
	}
	return group, id, k.Time().UTC(), true
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// SelectBackfill picks the newest images in keys that the gallery may publish
// under cfg, newest first, at most cfg.MaxItems (48 if unset). The returned
// items carry no prompt or kind yet.
func SelectBackfill(cfg Config, keys []string) ([]BackfillItem, BackfillStats) {
	max := cfg.MaxItems
	if max <= 0 {
		max = 48
	}
	var st BackfillStats
	var items []BackfillItem
	for _, key := range keys {
		st.Scanned++
		group, _, created, ok := ParseImageKey(key)
		switch {
		case !ok:
			st.NotImage++
		case group == DMPrefix:
			st.DM++
		case !isNumeric(group):
			st.Ambiguous++
		case !cfg.Eligible(group):
			st.GuildExcluded++
		default:
			items = append(items, BackfillItem{
				Submission: Submission{GuildID: group, ImageKey: key, Kind: KindGenerate},
				Created:    created,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].Created.Equal(items[j].Created) {
			return items[i].Created.After(items[j].Created)
		}
		return items[i].ImageKey > items[j].ImageKey
	})
	if len(items) > max {
		items = items[:max]
	}
	return items, st
}
