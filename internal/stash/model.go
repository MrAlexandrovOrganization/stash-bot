package stash

import "time"

type MediaType string

const (
	MediaTypeImage    MediaType = "image"
	MediaTypeVideo    MediaType = "video"
	MediaTypeGIF      MediaType = "gif"
	MediaTypeDocument MediaType = "document"
)

type Item struct {
	ID                 string    `json:"id"`
	Type               MediaType `json:"type"`
	FileName           string    `json:"file_name"`
	ContentType        string    `json:"content_type"`
	Size               int64     `json:"size"`
	Description        string    `json:"description"`
	Tags               []string  `json:"tags"`
	Source             string    `json:"source"`
	OriginalCaption    string    `json:"original_caption"`
	Transcript         *string   `json:"transcript,omitempty"`
	TranscriptJobID    *string   `json:"transcript_job_id,omitempty"`
	AIDescription      *string   `json:"ai_description,omitempty"`
	AIDescriptionError *string   `json:"ai_description_error,omitempty"`
	TelegramFileID     *string   `json:"telegram_file_id,omitempty"`
	FileUniqueID       *string   `json:"file_unique_id,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

type SearchQuery struct {
	Text string
	Tags []string
}

// SimilarRequest is the input to a hybrid similarity search against the stash
// backend. At least one of Text, ItemID or ImageBytes should be set.
type SimilarRequest struct {
	Text       string // natural-language query
	ItemID     string // find items visually similar to this existing item
	ImageBytes []byte // find items visually similar to an uploaded image
	Limit      int
	Offset     int
}

type UploadMeta struct {
	Description     string
	Tags            []string
	Source          string
	OriginalCaption string
	FileUniqueID    string
}

type UpdateMeta struct {
	Description    *string  `json:"description,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Transcript     *string  `json:"transcript,omitempty"`
	TelegramFileID *string  `json:"telegram_file_id,omitempty"`
	FileUniqueID   *string  `json:"file_unique_id,omitempty"`
}
