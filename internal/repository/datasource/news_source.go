package datasource

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/quant4dad/internal/domain"
)

// NewsSource abstracts a news feed: pull items newest-first and normalize them
// into domain.News. It is independent of the Client interface for bar data —
// news is an event stream, bars are a price time series. Implementations must
// give every News a stable (Source, ExternalID) pair so it can be deduplicated.
//
// Fetch receives seen: implementations should page from the newest end and stop
// as soon as a page contains an item seen reports as known, since that means the
// batch has caught up with the previous one. That avoids losing items pushed off
// the first page when a burst exceeds one page's capacity. Driving the paging
// with Paginate gives you this behavior for free.
//
// Name reports the source identifier. It is persisted as news.source, used as the
// key for pipeline subscriptions, and shown directly in the UI (verbatim unless a
// display name is configured). Pick something short, lowercase and stable, e.g.
// "myfeed".
type NewsSource interface {
	Name() string
	Fetch(ctx context.Context, seen SeenFunc) ([]*domain.News, error)
}

// SeenFunc reports whether an item has already been collected (is already
// stored). The collector injects it, backed by a DB lookup. True means known; a
// hit while paging is the signal to stop going further back.
type SeenFunc func(externalID string) bool

const (
	// NewsPageSize is the suggested per-page item count; sources can use it for
	// their own pageSize/limit parameter.
	NewsPageSize = 50
	// NewsTitleMaxLen matches the News.Title column (VARCHAR 512): anything
	// longer is truncated by character count, which avoids MySQL error 1406.
	NewsTitleMaxLen = 512
	// NewsMaxPages caps how many pages one collection pass walks. In steady
	// state seen is usually hit on page 1 or 2; this cap only matters on a first
	// collection into an empty table, or when catching up after a long pause,
	// where it stops an unbounded crawl back through history.
	NewsMaxPages = 20
)

var (
	newsSourceMu sync.RWMutex
	newsSources  = make(map[string]NewsSource)
)

// RegisterNewsSource records a news feed implementation, keyed by src.Name().
// The convention is to call this from the implementation package's init(), then
// blank-import that package from main. Registering the same name twice panics —
// a build-time programming error, better surfaced immediately.
func RegisterNewsSource(src NewsSource) {
	if src == nil {
		panic("datasource: RegisterNewsSource with nil source")
	}
	name := src.Name()
	if name == "" {
		panic("datasource: RegisterNewsSource with empty source name")
	}
	newsSourceMu.Lock()
	defer newsSourceMu.Unlock()
	if _, dup := newsSources[name]; dup {
		panic("datasource: duplicate news source registration: " + name)
	}
	newsSources[name] = src
}

// NewsSources returns a snapshot of the registry (source name -> implementation).
// Importing the providers package registers the bundled feeds. With no providers
// imported it returns an empty map and the collector has nothing to collect.
func NewsSources() map[string]NewsSource {
	newsSourceMu.RLock()
	defer newsSourceMu.RUnlock()
	out := make(map[string]NewsSource, len(newsSources))
	for name, src := range newsSources {
		out[name] = src
	}
	return out
}

// NewsSourceNames returns the registered source names in ascending order, for
// the service layer's subscription validation and the UI's source list.
func NewsSourceNames() []string {
	newsSourceMu.RLock()
	defer newsSourceMu.RUnlock()
	out := make([]string, 0, len(newsSources))
	for name := range newsSources {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// FetchPageFunc fetches one page of newest-first items and returns them along
// with the cursor for the next page. An empty cursor means the first page;
// page-number style sources can use the decimal page number as the cursor. An
// empty next means there are no more pages.
type FetchPageFunc func(ctx context.Context, cursor string) (items []*domain.News, next string, err error)

// Paginate is the shared paging driver: it fetches from the newest page and
// accumulates items until
//
//	(a) it hits an item seen reports as known (caught up with the previous batch),
//	(b) a page comes back empty or without a next cursor, or
//	(c) it reaches the NewsMaxPages cap.
//
// If paging fails only after some items were already collected, the error is
// swallowed and what was collected is returned — partial success beats losing the
// whole batch, and the gap is picked up next round. A NewsSource implementation
// only has to supply "fetch one page"; paging and catch-up semantics live here.
func Paginate(ctx context.Context, seen SeenFunc, fetch FetchPageFunc) ([]*domain.News, error) {
	out := make([]*domain.News, 0, NewsPageSize*2)
	inBatch := make(map[string]struct{})
	cursor := ""
	for page := 0; page < NewsMaxPages; page++ {
		items, next, err := fetch(ctx, cursor)
		if err != nil {
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		caughtUp := false
		for _, it := range items {
			if _, dup := inBatch[it.ExternalID]; dup {
				continue // duplicate across pages within this batch, skip
			}
			if seen != nil && seen(it.ExternalID) {
				caughtUp = true
				continue // known item: don't collect it, and mark caught up
			}
			inBatch[it.ExternalID] = struct{}{}
			out = append(out, it)
		}
		if caughtUp || next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return out, nil
}

// ClampTitle trims a title to the column's capacity by character count, ending
// with an ellipsis (which takes one of those characters). Some feeds publish
// headline-only items whose whole body serves as the title, and a body can exceed
// 512 characters; without clamping that trips MySQL error 1406. The full body
// lives in the Content (text) column, so truncating the title loses nothing.
func ClampTitle(title string) string {
	r := []rune(title)
	if len(r) <= NewsTitleMaxLen {
		return title
	}
	return string(r[:NewsTitleMaxLen-1]) + "…"
}

// ShanghaiLoc is the timezone commonly needed for A-share news timestamps,
// falling back to a fixed +8 offset if the tzdata lookup fails. Use it when a
// feed returns unix seconds or a local time string without a zone.
var ShanghaiLoc = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}()
