package internal

import "encoding/json"

// Kafka message payload
type KafkaMessage struct {
	RequestID  string          `json:"request_id"`
	Raw        json.RawMessage `json:"raw"`
	ReceivedAt int64           `json:"received_at"`
}

// Response represents unified Redis response structure
type Response struct {
	Status    string          `json:"status"`
	RequestID string          `json:"request_id"`
	Error     string          `json:"error,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	UpdatedAt int64           `json:"updated_at"`
}
