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
	MsgTypeAgentIntent   DesktopMessageType = "AGENT_INTENT"
	MsgTypeAgentResult   DesktopMessageType = "AGENT_RESULT"
)

type DesktopMessage struct {
	Type      DesktopMessageType `json:"type"`
	ID        string             `json:"id"`
	Timestamp int64              `json:"timestamp"`
	Payload   map[string]any     `json:"payload,omitempty"`
}

type DesktopCommandPayload struct {
	Action     string         `json:"action"` // "file.read", "file.write", "file.move", "file.list", "excel.read", "context.detect", "notify"
	Parameters map[string]any `json:"parameters"`
	TimeoutSec int            `json:"timeout_sec,omitempty"`
}

type DesktopCommandResultPayload struct {
	Success bool           `json:"success"`
	Data    map[string]any `json:"data,omitempty"`
	Error   string         `json:"error,omitempty"`
}

type DesktopEventPayload struct {
	EventName string         `json:"event_name"` // "file.created", "file.modified"
	Data      map[string]any `json:"data"`
}

// OSContext: Desktop Runner tarafından işletim sisteminden yakalanan anlık bağlam bilgisi.
type OSContext struct {
	ActiveWindow      string         `json:"active_window"`       // Örn: "explorer.exe - Faturalar"
	ActiveFolderPath  string         `json:"active_folder_path"`  // Örn: "C:\Muhasebe\Faturalar"
	SelectedFiles     []string       `json:"selected_files"`      // Seçili dosya isimleri
	DetectedFileTypes []string       `json:"detected_file_types"` // Klasörde tespit edilen uzantılar: [".xlsx", ".pdf"]
	SampleHeaders     []string       `json:"sample_headers"`      // İlk tablodan okunan örnek sütun başlıkları
	Metadata          map[string]any `json:"metadata,omitempty"`  // İlave işletim sistemi bağlamı
}
