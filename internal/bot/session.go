package bot

import "stash-bot/internal/stash"

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
	Items       []*stash.Item
	CurrentPage int

	// Item currently being viewed or edited.
	CurrentItem *stash.Item

	// Pending text input. Values: "desc", "tags", "tr", "search"
	Pending string
}
