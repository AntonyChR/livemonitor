package douyin

// LiveStatus represents the outcome of querying Douyin for a user's streaming state.
type LiveStatus struct {
	Status LiveStatusType `json:"status"`
	RoomID string         `json:"room_id,omitempty"`
}

// LiveStatusType represents "live" or "offline".
type LiveStatusType string

const (
	StatusLive    LiveStatusType = "live"
	StatusOffline LiveStatusType = "offline"
)
