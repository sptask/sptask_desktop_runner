package protocol

// WebSocket Mesaj Protokolü
type DesktopMessageType string

const (
	MsgTypeRegister      DesktopMessageType = "REGISTER"
	MsgTypeRegistered    DesktopMessageType = "REGISTERED"
	MsgTypePing          DesktopMessageType = "PING"
	MsgTypePong          DesktopMessageType = "PONG"
	MsgTypeCommand       DesktopMessageType = "COMMAND"
	MsgTypeCommandResult DesktopMessageType = "COMMAND_RESULT"
	MsgTypeEvent         DesktopMessageType = "EVENT"
	MsgTypeError         DesktopMessageType = "ERROR"
)

type DesktopMessage struct {
	Type      DesktopMessageType `json:"type"`
	ID        string             `json:"id"`
	Timestamp int64              `json:"timestamp"`
	Payload   map[string]any     `json:"payload,omitempty"`
}

type DesktopCommandPayload struct {
	Action     string         `json:"action"` // "file.read", "file.write", "file.move", "notify"
	Parameters map[string]any `json:"parameters"`
	TimeoutSec int            `json:"timeout_sec,omitempty"`
}

type DesktopCommandResultPayload struct {
	Success bool           `json:"success"`
	Data    map[string]any `json:"data,omitempty"`
	Error   string         `json:"error,omitempty"`
}

type DesktopEventPayload struct {
	EventName string         `json:"event_name"` // "desktop.file_created"
	Data      map[string]any `json:"data"`
}
