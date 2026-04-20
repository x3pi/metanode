package ws_writer

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

type WebSocketWriter struct {
	conn  *websocket.Conn
	mutex sync.Mutex
}

// NewWebSocketWriter creates a new WebSocketWriter
func NewWebSocketWriter(conn *websocket.Conn) *WebSocketWriter {
	return &WebSocketWriter{conn: conn}
}

// WriteJSON writes JSON message with timeout and mutex protection
func (w *WebSocketWriter) WriteJSON(v interface{}) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if err := w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		logger.Warn("Error setting write deadline: %v", err)
	}
	defer w.conn.SetWriteDeadline(time.Time{})

	return w.conn.WriteJSON(v)
}

// WriteMessage writes binary message with timeout and mutex protection
func (w *WebSocketWriter) WriteMessage(messageType int, data []byte) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if err := w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		logger.Warn("Error setting write deadline: %v", err)
	}
	defer w.conn.SetWriteDeadline(time.Time{})

	return w.conn.WriteMessage(messageType, data)
}

// WriteCloseMessage sends close message with custom code and text
func (w *WebSocketWriter) WriteCloseMessage(closeCode int, text string) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if err := w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		logger.Warn("Error setting write deadline (close): %v", err)
	}
	defer w.conn.SetWriteDeadline(time.Time{})

	msg := websocket.FormatCloseMessage(closeCode, text)
	return w.conn.WriteMessage(websocket.CloseMessage, msg)
}
