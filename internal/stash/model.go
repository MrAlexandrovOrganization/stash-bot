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
	ID          string    `json:"id"`
	Type        MediaType `json:"type"`
	FileName    string    `json:"file_name"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	Description string    `json:"description"`
	Tags        []string  `json:"tags"`
	Transcript  *string   `json:"transcript,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type SearchQuery struct {
	Text string
	Tags []string
}

type UploadMeta struct {
	Description string
	Tags        []string
}

type UpdateMeta struct {
	Description *string  `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Transcript  *string  `json:"transcript,omitempty"`
}
