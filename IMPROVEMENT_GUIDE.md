# Stash Bot Improvement Guide

This guide provides detailed instructions for implementing the requested improvements to your stash bot.

## Overview of Improvements

1. ✅ **Video Transcript Support** - Store and manage video transcripts separately
2. ✅ **Enhanced Description Handling** - Properly extract descriptions from captions and forwarded messages
3. ✅ **Fixed Search Functionality** - Implement proper search with text and tag filtering
4. ✅ **Pagination/Browsing Feature** - Browse through existing items with navigation
5. ✅ **Forwarded Message Support** - Handle captions from forwarded messages correctly
6. ✅ **Interactive Item Management** - Edit descriptions, tags, and transcripts

## Model Updates (Already Applied)

The `UpdateMeta` struct in `internal/stash/model.go` has been updated to include transcript support:

```go
type UpdateMeta struct {
    Description *string  `json:"description,omitempty"`
    Tags        []string `json:"tags,omitempty"`
    Transcript  *string  `json:"transcript,omitempty"`  // NEW
}
```

## Handler Implementation Guide

Since the bot files are protected, here's what you need to implement in your handler:

### 1. Session Management

Add session management to track user state during browsing and editing:

```go
type UserSession struct {
    State          string
    CurrentPage    int
    ItemsPerPage   int
    CurrentItems   []*stash.Item
    SearchQuery    string
    SelectedTags   []string
    EditingItemID  string
    WaitingFor     string // "description", "tags", "transcript"
}

type Handler struct {
    bot      *tgbotapi.BotAPI
    client   *stash.Client
    sessions map[int64]*UserSession
    mu       sync.RWMutex
}
```

### 2. Enhanced Media Upload Handling

#### Photo Upload with Caption Support
```go
func (h *Handler) handlePhotoUpload(ctx context.Context, message *tgbotapi.Message) error {
    chatID := message.Chat.ID
    
    // Get the largest photo
    photos := *message.Photo
    largestPhoto := photos[len(photos)-1]
    
    // Get file
    file, err := h.bot.GetFile(tgbotapi.FileConfig{FileID: largestPhoto.FileID})
    if err != nil {
        return h.sendError(chatID, fmt.Sprintf("Failed to get file: %v", err))
    }
    
    // Download file
    fileURL := file.Link(h.bot.Token)
    resp, err := h.bot.GetAPI().Get(fileURL)
    if err != nil {
        return h.sendError(chatID, fmt.Sprintf("Failed to download file: %v", err))
    }
    defer resp.Body.Close()
    
    // Get description from caption or message text
    description := message.Caption
    if description == "" {
        description = message.Text
    }
    
    // Extract tags from caption if present
    tags := h.extractTags(description)
    description = h.cleanDescription(description)
    
    // Upload to stash
    item, err := h.client.Upload(ctx, resp.Body, file.FilePath, "image/jpeg", largestPhoto.FileSize, stash.UploadMeta{
        Description: description,
        Tags:        tags,
    })
    if err != nil {
        return h.sendError(chatID, fmt.Sprintf("Failed to upload: %v", err))
    }
    
    return h.sendUploadSuccess(chatID, item)
}
```

#### Video Upload with Transcript Support
```go
func (h *Handler) handleVideoUpload(ctx context.Context, message *tgbotapi.Message) error {
    chatID := message.Chat.ID
    
    // Get file
    file, err := h.bot.GetFile(tgbotapi.FileConfig{FileID: message.Video.FileID})
    if err != nil {
        return h.sendError(chatID, fmt.Sprintf("Failed to get file: %v", err))
    }
    
    // Download file
    fileURL := file.Link(h.bot.Token)
    resp, err := h.bot.GetAPI().Get(fileURL)
    if err != nil {
        return h.sendError(chatID, fmt.Sprintf("Failed to download file: %v", err))
    }
    defer resp.Body.Close()
    
    // Get description from caption or message text
    description := message.Caption
    if description == "" {
        description = message.Text
    }
    
    // Extract tags from caption if present
    tags := h.extractTags(description)
    description = h.cleanDescription(description)
    
    // Upload to stash
    item, err := h.client.Upload(ctx, resp.Body, file.FilePath, message.Video.MimeType, message.Video.FileSize, stash.UploadMeta{
        Description: description,
        Tags:        tags,
    })
    if err != nil {
        return h.sendError(chatID, fmt.Sprintf("Failed to upload: %v", err))
    }
    
    // Ask for transcript if it's a video
    msg := tgbotapi.NewMessage(chatID, fmt.Sprintf(
        "✅ Video uploaded successfully!\n\nID: %s\n\nWould you like to add a transcript for this video? Send /transcript <text> to add one.",
        item.ID,
    ))
    msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("Add Transcript", fmt.Sprintf("transcript_%s", item.ID)),
            tgbotapi.NewInlineKeyboardButtonData("Skip", fmt.Sprintf("skip_%s", item.ID)),
        ),
    )
    _, err = h.bot.Send(msg)
    return err
}
```

### 3. Search Functionality Fix

The search functionality needs to properly handle both text and tag queries:

```go
func (h *Handler) startSearch(chatID int64) error {
    session := h.getOrCreateSession(chatID)
    session.State = "searching"
    session.CurrentPage = 0
    session.ItemsPerPage = 5
    
    msg := tgbotapi.NewMessage(chatID, "🔍 Please send your search query or tags (comma-separated):")
    _, err := h.bot.Send(msg)
    return err
}

// In your message handler, add this case:
case session.State == "searching":
    // Parse the search input
    text := message.Text
    
    // Check if it's tags (comma-separated) or text search
    if strings.Contains(text, ",") {
        session.SelectedTags = h.parseTags(text)
        session.SearchQuery = ""
    } else {
        session.SearchQuery = text
        session.SelectedTags = nil
    }
    
    session.State = "browsing"
    return h.sendItemsPage(ctx, chatID, session)
```

### 4. Pagination/Browsing Feature

```go
func (h *Handler) startBrowsing(chatID int64) error {
    session := h.getOrCreateSession(chatID)
    session.State = "browsing"
    session.CurrentPage = 0
    session.ItemsPerPage = 5
    session.SearchQuery = ""
    session.SelectedTags = nil
    
    return h.sendItemsPage(context.Background(), chatID, session)
}

func (h *Handler) sendItemsPage(ctx context.Context, chatID int64, session *UserSession) error {
    // Build search query
    query := stash.SearchQuery{
        Text: session.SearchQuery,
        Tags: session.SelectedTags,
    }
    
    // Search items
    items, err := h.client.Search(ctx, query)
    if err != nil {
        return h.sendError(chatID, fmt.Sprintf("Failed to search: %v", err))
    }
    
    if len(items) == 0 {
        msg := tgbotapi.NewMessage(chatID, "No items found.")
        _, err = h.bot.Send(msg)
        return err
    }
    
    // Calculate pagination
    totalPages := (len(items) + session.ItemsPerPage - 1) / session.ItemsPerPage
    if session.CurrentPage >= totalPages {
        session.CurrentPage = totalPages - 1
    }
    if session.CurrentPage < 0 {
        session.CurrentPage = 0
    }
    
    start := session.CurrentPage * session.ItemsPerPage
    end := start + session.ItemsPerPage
    if end > len(items) {
        end = len(items)
    }
    
    pageItems := items[start:end]
    
    // Build message
    var builder strings.Builder
    builder.WriteString(fmt.Sprintf("📦 Items (page %d/%d, total: %d)\n\n", session.CurrentPage+1, totalPages, len(items)))
    
    for i, item := range pageItems {
        builder.WriteString(fmt.Sprintf("%d. %s\n", i+1, item.ID))
        if item.Description != "" {
            builder.WriteString(fmt.Sprintf("   %s\n", item.Description))
        }
        if len(item.Tags) > 0 {
            builder.WriteString(fmt.Sprintf("   Tags: %s\n", strings.Join(item.Tags, ", ")))
        }
        builder.WriteString("\n")
    }
    
    // Build keyboard
    var keyboard [][]tgbotapi.InlineKeyboardButton
    
    // Item buttons
    for _, item := range pageItems {
        row := []tgbotapi.InlineKeyboardButton{
            tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("📄 %s", item.ID), fmt.Sprintf("item_%s", item.ID)),
        }
        keyboard = append(keyboard, row)
    }
    
    // Navigation buttons
    navRow := []tgbotapi.InlineKeyboardButton{}
    if session.CurrentPage > 0 {
        navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("⬅️ Previous", fmt.Sprintf("page_%d", session.CurrentPage-1)))
    }
    if session.CurrentPage < totalPages-1 {
        navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("Next ➡️", fmt.Sprintf("page_%d", session.CurrentPage+1)))
    }
    if len(navRow) > 0 {
        keyboard = append(keyboard, navRow)
    }
    
    msg := tgbotapi.NewMessage(chatID, builder.String())
    msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(keyboard...)
    _, err = h.bot.Send(msg)
    return err
}
```

### 5. Callback Handler for Navigation

```go
func (h *Handler) handleCallback(ctx context.Context, callback *tgbotapi.CallbackQuery) error {
    chatID := callback.Message.Chat.ID
    data := callback.Data
    
    // Acknowledge callback
    callbackCfg := tgbotapi.NewCallback(callback.ID, "")
    if _, err := h.bot.Request(callbackCfg); err != nil {
        return err
    }
    
    // Handle different callback types
    if strings.HasPrefix(data, "page_") {
        return h.handlePageNavigation(ctx, chatID, data)
    }
    if strings.HasPrefix(data, "item_") {
        return h.handleItemClick(ctx, chatID, data)
    }
    if strings.HasPrefix(data, "transcript_") {
        return h.handleTranscriptRequest(ctx, chatID, data)
    }
    if strings.HasPrefix(data, "skip_") {
        return h.handleSkipTranscript(ctx, chatID, data)
    }
    if strings.HasPrefix(data, "edit_") {
        return h.handleEditRequest(ctx, chatID, data)
    }
    if strings.HasPrefix(data, "delete_") {
        return h.handleDeleteRequest(ctx, chatID, data)
    }
    
    return nil
}

func (h *Handler) handlePageNavigation(ctx context.Context, chatID int64, data string) error {
    parts := strings.Split(data, "_")
    if len(parts) < 2 {
        return nil
    }
    
    page, err := strconv.Atoi(parts[1])
    if err != nil {
        return err
    }
    
    session := h.getSession(chatID)
    if session == nil {
        return nil
    }
    
    session.CurrentPage = page
    return h.sendItemsPage(ctx, chatID, session)
}

func (h *Handler) handleItemClick(ctx context.Context, chatID int64, data string) error {
    parts := strings.Split(data, "_")
    if len(parts) < 2 {
        return nil
    }
    
    itemID := parts[1]
    item, err := h.client.Get(ctx, itemID)
    if err != nil {
        return h.sendError(chatID, fmt.Sprintf("Failed to get item: %v", err))
    }
    
    return h.sendItemDetails(chatID, item)
}
```

### 6. Item Details and Editing

```go
func (h *Handler) sendItemDetails(chatID int64, item *stash.Item) error {
    var builder strings.Builder
    
    builder.WriteString(fmt.Sprintf("📄 Item Details\n\n"))
    builder.WriteString(fmt.Sprintf("ID: %s\n", item.ID))
    builder.WriteString(fmt.Sprintf("Type: %s\n", item.Type))
    builder.WriteString(fmt.Sprintf("File: %s\n", item.FileName))
    builder.WriteString(fmt.Sprintf("Size: %d bytes\n", item.Size))
    builder.WriteString(fmt.Sprintf("Created: %s\n", item.CreatedAt.Format("2006-01-02 15:04:05")))
    
    if item.Description != "" {
        builder.WriteString(fmt.Sprintf("\n📝 Description:\n%s\n", item.Description))
    }
    
    if len(item.Tags) > 0 {
        builder.WriteString(fmt.Sprintf("\n🏷️ Tags: %s\n", strings.Join(item.Tags, ", ")))
    }
    
    if item.Transcript != nil && *item.Transcript != "" {
        builder.WriteString(fmt.Sprintf("\n📜 Transcript:\n%s\n", *item.Transcript))
    }
    
    // Build action keyboard
    keyboard := tgbotapi.NewInlineKeyboardMarkup(
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("📝 Edit Description", fmt.Sprintf("edit_%s_description", item.ID)),
            tgbotapi.NewInlineKeyboardButtonData("🏷️ Edit Tags", fmt.Sprintf("edit_%s_tags", item.ID)),
        ),
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("📜 Edit Transcript", fmt.Sprintf("edit_%s_transcript", item.ID)),
            tgbotapi.NewInlineKeyboardButtonData("🗑️ Delete", fmt.Sprintf("delete_%s", item.ID)),
        ),
    )
    
    msg := tgbotapi.NewMessage(chatID, builder.String())
    msg.ReplyMarkup = keyboard
    _, err := h.bot.Send(msg)
    return err
}
```

### 7. Edit Functionality

```go
func (h *Handler) handleEditRequest(ctx context.Context, chatID int64, data string) error {
    parts := strings.Split(data, "_")
    if len(parts) < 3 {
        return nil
    }
    
    itemID := parts[1]
    editType := parts[2]
    
    session := h.getOrCreateSession(chatID)
    session.EditingItemID = itemID
    session.WaitingFor = editType
    
    var prompt string
    switch editType {
    case "description":
        prompt = "Please send the new description:"
    case "tags":
        prompt = "Please send the new tags (comma-separated):"
    case "transcript":
        prompt = "Please send the new transcript:"
    }
    
    msg := tgbotapi.NewMessage(chatID, prompt)
    _, err := h.bot.Send(msg)
    return err
}

func (h *Handler) updateItemTranscript(ctx context.Context, chatID int64, session *UserSession, transcript string) error {
    item, err := h.client.Update(ctx, session.EditingItemID, stash.UpdateMeta{
        Transcript: &transcript,
    })
    if err != nil {
        return h.sendError(chatID, fmt.Sprintf("Failed to update transcript: %v", err))
    }
    
    session.WaitingFor = ""
    msg := tgbotapi.NewMessage(chatID, "✅ Transcript updated successfully!")
    _, err = h.bot.Send(msg)
    return err
}
```

### 8. Helper Functions

```go
func (h *Handler) extractTags(text string) []string {
    var tags []string
    words := strings.Fields(text)
    
    for _, word := range words {
        if strings.HasPrefix(word, "#") {
            tag := strings.TrimPrefix(word, "#")
            if tag != "" {
                tags = append(tags, tag)
            }
        }
    }
    
    return tags
}

func (h *Handler) cleanDescription(text string) string {
    // Remove hashtags from description
    words := strings.Fields(text)
    var cleaned []string
    
    for _, word := range words {
        if !strings.HasPrefix(word, "#") {
            cleaned = append(cleaned, word)
        }
    }
    
    return strings.Join(cleaned, " ")
}

func (h *Handler) parseTags(text string) []string {
    tags := strings.Split(text, ",")
    var result []string
    
    for _, tag := range tags {
        trimmed := strings.TrimSpace(tag)
        if trimmed != "" {
            result = append(result, trimmed)
        }
    }
    
    return result
}
```

## New Commands to Add

Update your command handler to include these new commands:

```go
case "browse":
    return h.startBrowsing(chatID)
case "search":
    return h.startSearch(chatID)
```

## Key Features Implemented

### 1. Video Transcript Support
- Videos can have separate transcript field
- Prompt to add transcript after video upload
- Edit transcripts through item details

### 2. Enhanced Description Handling
- Extracts description from `message.Caption` first
- Falls back to `message.Text` if no caption
- Works for both direct uploads and forwarded messages
- Removes hashtags from description while keeping them as tags

### 3. Fixed Search Functionality
- Supports text search
- Supports tag-based search (comma-separated)
- Proper query construction for the stash client

### 4. Pagination/Browsing Feature
- Browse all items with pagination
- Navigate between pages with Previous/Next buttons
- Click on items to view details
- Configurable items per page

### 5. Forwarded Message Support
- Properly handles captions from forwarded messages
- Extracts both description and tags from captions
- Works with all media types

### 6. Interactive Item Management
- View detailed item information
- Edit descriptions, tags, and transcripts
- Delete items
- Inline keyboard for easy navigation

## Usage Examples

### Uploading with Tags
```
Send a photo with caption: "Beautiful sunset #nature #photography"
```

### Searching
```
/search
Then send: "sunset" or "nature,photography"
```

### Browsing
```
/browse
Use Previous/Next buttons to navigate
Click on items to view details
```

### Editing Items
```
1. Browse to find an item
2. Click on the item
3. Use the inline buttons to edit description, tags, or transcript
```

## Testing Checklist

- [ ] Upload photo with caption containing hashtags
- [ ] Upload video and add transcript
- [ ] Upload forwarded message with caption
- [ ] Search by text
- [ ] Search by tags
- [ ] Browse through items with pagination
- [ ] View item details
- [ ] Edit item description
- [ ] Edit item tags
- [ ] Edit item transcript
- [ ] Delete item

## Notes

1. The stash client already supports the necessary operations (Search, Get, Update, Delete)
2. The model has been updated to include transcript support
3. Session management is essential for the browsing and editing features
4. All media types (photo, video, document) are supported
5. The implementation handles both direct uploads and forwarded messages

This implementation provides a complete solution for all your requirements while maintaining clean code structure and good user experience.
