package bot

import "stash-bot/internal/stash"

type BrowseKind string

const (
	BrowseStorage BrowseKind = "storage"
	BrowseSearch  BrowseKind = "search"
	BrowseSimilar BrowseKind = "similar"
)

// Screen identifies the current UI screen.
type Screen string

const (
	ScreenMain    Screen = "main"
	ScreenStorage Screen = "storage" // browsing storage or search results
	ScreenSelect  Screen = "select"  // picking an item from current page
	ScreenItem    Screen = "item"    // item detail view
)

// Session holds per-user navigation state.
type Session struct {
	ChatID int64
	Screen Screen
	Back   Screen // where the Back button goes

	// Items loaded for current browsing context (storage or search results).
	Items        []*stash.Item
	CurrentPage  int
	Browse       BrowseKind
	Query        stash.SearchQuery
	Paged        bool
	Total        int
	SimilarQuery stash.SimilarRequest

	// Item currently being viewed or edited.
	CurrentItem *stash.Item

	// Pending text input. Values: "desc", "tags", "tr", "search", "simq", "simimg"
	Pending string

	// LastMsgID is the message ID of the bot control message (text+keyboard).
	// Used to edit the message in place instead of sending a new one.
	LastMsgID int

	// MediaMsgIDs holds message IDs of the currently displayed page media.
	// Populated when files are sent; cleared when navigating away from storage.
	MediaMsgIDs []int
}
