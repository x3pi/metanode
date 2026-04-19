package trace

import (
	"context"
	"encoding/json"

	// "encoding/json" // Bỏ import nếu không dùng JSON trong module này
	// Giữ lại để log lỗi hoặc cảnh báo
	"sync"
	"time"

	"github.com/google/uuid" // Đảm bảo đã `go get github.com/google/uuid`
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// --- Định nghĩa Context Keys ---
type contextKey string

const spanKey contextKey = "currentSpan" // Key cho span hiện tại trong context
// Key mới để lưu con trỏ đến SpanCollector trong context
const spanCollectorKey contextKey = "spanCollectorTarget"

// --- SpanCollector: Struct để thu thập Spans một cách an toàn ---
// SpanCollector giữ một slice các spans và một mutex để bảo vệ truy cập đồng thời.
type SpanCollector struct {
	mu    sync.Mutex // Mutex bảo vệ slice Spans
	Spans []*Span    // Slice chứa các span đã thu thập
}

// NewSpanCollector tạo một SpanCollector mới, rỗng.
// Bạn sẽ tạo một đối tượng này trước khi bắt đầu trace.
func NewSpanCollector() *SpanCollector {
	return &SpanCollector{
		Spans: make([]*Span, 0, 100), // Khởi tạo với capacity ban đầu (ví dụ: 100)
	}
}

// Add thêm một span vào collector một cách an toàn (goroutine-safe).
// Phương thức này được gọi tự động bởi span.End() nếu collector được tìm thấy trong context.
func (sc *SpanCollector) Add(s *Span) {
	if sc == nil { // Không làm gì nếu collector là nil
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.Spans = append(sc.Spans, s)
	// Tùy chọn: Log khi một span được thêm vào
	// log.Printf("[Collector] Added span '%s' (ID: %s). Total: %d\n", s.Name, s.Context.SpanID, len(sc.Spans))
}

// GetSpans trả về một bản sao của tất cả các spans đã được thu thập trong collector này.
// Trả về nil nếu chưa có span nào.
// Nên gọi hàm này sau khi bạn chắc chắn rằng tất cả các hoạt động của trace đã hoàn thành.
func (sc *SpanCollector) GetSpans() []*Span {
	if sc == nil {
		return nil
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if len(sc.Spans) == 0 {
		return nil
	}

	// Tạo bản sao để đảm bảo slice trả về không bị thay đổi bởi các thao tác Add tiếp theo
	spansCopy := make([]*Span, len(sc.Spans))
	copy(spansCopy, sc.Spans)
	return spansCopy
}

// Count trả về số lượng span hiện có trong collector.
func (sc *SpanCollector) Count() int {
	if sc == nil {
		return 0
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return len(sc.Spans)
}

// Clear xóa tất cả các span đã thu thập khỏi collector.
func (sc *SpanCollector) Clear() {
	if sc == nil {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.Spans = []*Span{} // Tạo slice mới rỗng
}

// --- Định nghĩa Span và các thành phần liên quan (như mã gốc của bạn) ---

// SpanContext chứa thông tin định danh của một span.
type SpanContext struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
}

// Span đại diện cho một đơn vị công việc hoặc hoạt động trong một trace.
type Span struct {
	ctx          context.Context        // Context chứa span này VÀ collector (nếu có)
	mu           sync.Mutex             // Bảo vệ các trường nội bộ của span
	Name         string                 `json:"name"`
	Context      SpanContext            `json:"context"`
	ParentSpanID string                 `json:"parentSpanId,omitempty"`
	StartTime    time.Time              `json:"startTime"`
	EndTime      time.Time              `json:"endTime,omitempty"`
	Duration     time.Duration          `json:"durationNs,omitempty"`
	Attributes   map[string]interface{} `json:"attributes,omitempty"`
	Events       []Event                `json:"events,omitempty"`
	Error        error                  `json:"-"`
	ErrorMsg     string                 `json:"errorMsg,omitempty"`
	ended        bool
}

// Event đại diện cho một sự kiện xảy ra tại một thời điểm trong một span.
type Event struct {
	Name       string                 `json:"name"`
	Attributes map[string]interface{} `json:"attributes,omitempty"`
}

// --- Các hàm tạo và quản lý Span ---

// NewTrace bắt đầu một trace mới và span gốc của nó.
// Nó nhận vào một con trỏ đến SpanCollector (có thể là nil).
// Collector này sẽ được nhúng vào context trả về.
// Trả về context mới chứa span gốc (và collector) cùng con trỏ đến span gốc.
func NewTrace(ctx context.Context, name string, attributes map[string]interface{}, collector *SpanCollector) (context.Context, *Span) {
	traceID := uuid.NewString()
	spanID := uuid.NewString()

	span := &Span{
		Name: name,
		Context: SpanContext{
			TraceID: traceID,
			SpanID:  spanID,
		},
		StartTime:  time.Now(),
		Attributes: make(map[string]interface{}),
		Events:     make([]Event, 0, 5),
	}

	for k, v := range attributes {
		span.Attributes[k] = v
	}

	// Tạo context mới chứa span này
	newCtx := context.WithValue(ctx, spanKey, span)
	// Nếu có collector, nhúng nó vào context mới
	if collector != nil {
		newCtx = context.WithValue(newCtx, spanCollectorKey, collector)
	}
	// QUAN TRỌNG: Gán context cuối cùng (có thể chứa cả span và collector) cho span
	span.ctx = newCtx

	span.AddEvent("TraceStarted", nil)
	// log.Printf("[TRACE] New trace started. Name: '%s', TraceID: %s, Collector provided: %v\n", name, traceID, collector != nil)
	return newCtx, span
}

// StartSpan bắt đầu một span con mới trong context của span cha.
// Nó tự động kế thừa SpanCollector từ context cha.
// Trả về context mới chứa span con và con trỏ đến span đó.
func StartSpan(ctx context.Context, name string, attributes map[string]interface{}) (context.Context, *Span) {
	parentSpan, ok := ctx.Value(spanKey).(*Span)
	if !ok || parentSpan == nil {
		// Không tìm thấy span cha. Tạo trace mới nhưng cố gắng kế thừa collector.
		collector, _ := ctx.Value(spanCollectorKey).(*SpanCollector)
		// log.Printf("[TRACE-WARN] Starting new trace for '%s' via StartSpan (no parent span). Inheriting collector: %v\n", name, collector != nil)
		// Gọi NewTrace, truyền collector lấy được từ context hiện tại
		return NewTrace(ctx, name, attributes, collector)
	}

	// Tạo span con
	spanID := uuid.NewString()
	span := &Span{
		Name: name,
		Context: SpanContext{
			TraceID: parentSpan.Context.TraceID, // Thừa kế TraceID
			SpanID:  spanID,
		},
		ParentSpanID: parentSpan.Context.SpanID, // Liên kết với cha
		StartTime:    time.Now(),
		Attributes:   make(map[string]interface{}),
		Events:       make([]Event, 0, 5),
	}

	for k, v := range attributes {
		span.Attributes[k] = v
	}

	// Tạo context mới chứa span con. Context này sẽ tự động chứa collector
	// vì nó được tạo từ context cha (ctx) vốn đã chứa collector.
	newCtx := context.WithValue(ctx, spanKey, span)
	// Gán context mới cho span con
	span.ctx = newCtx

	span.AddEvent("SpanStarted", nil)
	return newCtx, span
}

// SpanFromContext lấy span hiện tại từ context (nếu có).
func SpanFromContext(ctx context.Context) (*Span, bool) {
	span, ok := ctx.Value(spanKey).(*Span)
	return span, ok && span != nil
}

// End hoàn thành span, tính toán thời lượng và thêm span vào collector (nếu có trong context).
func (s *Span) End() {
	s.mu.Lock() // Khóa để cập nhật trạng thái span

	if s.ended { // Tránh kết thúc nhiều lần
		s.mu.Unlock()
		return
	}

	s.EndTime = time.Now()
	s.Duration = s.EndTime.Sub(s.StartTime)
	if s.Error != nil { // Cập nhật ErrorMsg nếu có lỗi
		s.ErrorMsg = s.Error.Error()
	}
	s.ended = true

	// Lấy context của span TRƯỚC KHI mở khóa
	ctx := s.ctx

	s.mu.Unlock() // Mở khóa span

	// --- Logic Thu thập ---
	// Lấy collector từ context của span
	if collector, ok := ctx.Value(spanCollectorKey).(*SpanCollector); ok && collector != nil {
		// Nếu tìm thấy collector hợp lệ, gọi phương thức Add của nó
		logger.Info(s.ToJSONString())
		collector.Add(s)
	} else {
		// Không tìm thấy collector, có thể log hoặc bỏ qua
		// log.Printf("[TRACE] Span '%s' (ID: %s) ended, no collector in context.\n", s.Name, s.Context.SpanID)
	}
}

// AddEvent thêm một sự kiện vào span.
func (s *Span) AddEvent(name string, attributes map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	var attrsCopy map[string]interface{}
	if len(attributes) > 0 {
		attrsCopy = make(map[string]interface{}, len(attributes))
		for k, v := range attributes {
			attrsCopy[k] = v
		}
	}
	event := Event{
		Name:       name,
		Attributes: attrsCopy,
	}
	s.Events = append(s.Events, event)
}

// SetAttribute thêm hoặc cập nhật một thuộc tính trên span.
func (s *Span) SetAttribute(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	if s.Attributes == nil {
		s.Attributes = make(map[string]interface{})
	}
	s.Attributes[key] = value
}

// SetError ghi lại một lỗi cho span.
func (s *Span) SetError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.Error = err
	s.ErrorMsg = err.Error()
	if s.Attributes == nil {
		s.Attributes = make(map[string]interface{})
	}
	s.Attributes["error"] = true
	errorEvent := Event{
		Name: "error",
		Attributes: map[string]interface{}{
			"message": s.ErrorMsg,
		},
	}
	s.Events = append(s.Events, errorEvent)
}

// GetContext trả về context.Context liên kết với span này.
func (s *Span) GetContext() context.Context {
	return s.ctx
}

func (s *Span) ToJSONString() string {
	s.mu.Lock() // Khóa để đọc trạng thái nhất quán
	defer s.mu.Unlock()

	// MarshalIndent sẽ tự động sử dụng các json tag đã định nghĩa trong struct Span và Event.
	// Các trường không có tag hoặc có tag `json:"-"` sẽ bị bỏ qua.
	// Tham số thứ 2 là tiền tố (prefix) cho mỗi dòng (để trống).
	// Tham số thứ 3 là chuỗi dùng để thụt lề (ví dụ: "  " cho 2 dấu cách).
	jsonData, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		// Ghi log lỗi nếu không thể marshal
		// log.Printf("Error marshaling Span (ID: %s) to JSON: %v", s.Context.SpanID, err)
		// Trả về một JSON lỗi đơn giản hoặc chuỗi rỗng
		return `{"error": "failed to marshal span to JSON"}`
		// return ""
	}

	return string(jsonData)
}
