# Stash Bot - Quick Reference Guide

## What's Been Done

### ✅ Model Update
- Added `Transcript` field to [`UpdateMeta`](internal/stash/model.go:36) struct
- Now supports storing video transcripts separately

### ✅ Comprehensive Implementation Guide
- Created [`IMPROVEMENT_GUIDE.md`](IMPROVEMENT_GUIDE.md) with detailed implementation instructions

## Key Features to Implement

### 1. Video Transcript Support
- Videos can have separate transcript field
- Prompt to add transcript after video upload
- Edit transcripts through item details

### 2. Enhanced Description Handling
- Extracts description from `message.Caption` first
- Falls back to `message.Text` if no caption
- Works for both direct uploads and forwarded messages
- Removes hashtags from description while keeping them as tags
- Displays AI-generated (neuro) descriptions from the stash backend

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

## New Commands

```
/browse    - Browse all items with pagination
/search    - Search by text or tags
```

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

## Implementation Priority

1. **Session Management** - Required for browsing and editing
2. **Enhanced Upload Handlers** - Support captions and tags
3. **Search Functionality** - Fix search with proper query handling
4. **Pagination/Browsing** - Implement item browsing with navigation
5. **Callback Handlers** - Handle inline button interactions
6. **Edit Functionality** - Allow editing descriptions, tags, transcripts

## Files to Modify

Since bot files are protected by `.codeassistantignore`, you'll need to manually update:

1. `internal/bot/handler.go` - Main handler implementation
2. `cmd/bot/main.go` - Command registration

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

## Next Steps

1. Review [`IMPROVEMENT_GUIDE.md`](IMPROVEMENT_GUIDE.md) for detailed implementation
2. Update your bot handler following the guide
3. Test each feature using the checklist
4. Adjust pagination settings as needed (default: 5 items per page)

## Notes

- The stash client already supports all necessary operations
- The model has been updated to include transcript support
- Session management is essential for browsing and editing features
- All media types (photo, video, document) are supported
- The implementation handles both direct uploads and forwarded messages
