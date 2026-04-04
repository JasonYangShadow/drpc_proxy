package internal

import "errors"

type RPCRequest struct {
	ID      int64  `json:"id"`
	JsonRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

func (r *RPCRequest) Validate() error {
	if r.JsonRPC != "2.0" {
		return errors.New("invalid jsonrpc version, must be 2.0")
	}
	if r.Method == "" {
		return errors.New("method is required")
	}
	return nil
}

type RPCResponse struct {
	ID      int64  `json:"id"`
	JsonRPC string `json:"jsonrpc"`
	Result  any    `json:"result,omitempty"`
	Error   error  `json:"error,omitempty"`
}

// Kafka message payload
type KafkaMessage struct {
	RequestID  string     `json:"request_id"`
	RPCRequest RPCRequest `json:"rpc_request"`
	ReceivedAt int64      `json:"received_at"`
}
