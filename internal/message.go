package internal

import "encoding/json"

// Kafka message payload
type KafkaMessage struct {
	RequestID  string          `json:"request_id"`
	Raw        json.RawMessage `json:"raw"`
	ReceivedAt int64           `json:"received_at"`
}
