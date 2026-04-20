package core

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// EventType defines the type of event
type EventType string

const (
	EventTick           EventType = "TICK"
	EventOrderUpdate    EventType = "ORDER_UPDATE"
	EventPositionUpdate EventType = "POSITION_UPDATE"
	EventLog            EventType = "LOG"
	EventStart          EventType = "START"
	EventStop           EventType = "STOP"
)

// TickData carries tick information for multi-symbol support
type TickData struct {
	Symbol string
	Price  float64
}

// Event carries data
type Event struct {
	Type      EventType
	Data      interface{}
	Timestamp time.Time
}

// EventHandler processes an event
type EventHandler func(ctx context.Context, event Event) error

// EventBus manages subscriptions and publications
type EventBus struct {
	mu       sync.RWMutex
	handlers map[EventType][]EventHandler
	queue    chan Event
	ctx      context.Context
	cancel   context.CancelFunc
}

func NewEventBus() *EventBus {
	ctx, cancel := context.WithCancel(context.Background())
	return &EventBus{
		handlers: make(map[EventType][]EventHandler),
		queue:    make(chan Event, 1000),
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Subscribe adds a handler for an event type
func (eb *EventBus) Subscribe(eventType EventType, handler EventHandler) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.handlers[eventType] = append(eb.handlers[eventType], handler)
}

// SubscribeWithFilter adds a handler that only processes events for a specific symbol
func (eb *EventBus) SubscribeWithFilter(eventType EventType, symbol string, handler EventHandler) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.handlers[eventType] = append(eb.handlers[eventType], func(ctx context.Context, event Event) error {
		switch data := event.Data.(type) {
		case TickData:
			if data.Symbol == symbol {
				return handler(ctx, event)
			}
		case *TickData:
			if data.Symbol == symbol {
				return handler(ctx, event)
			}
		default:
			return handler(ctx, event)
		}
		return nil
	})
}

// Publish sends an event to the queue
func (eb *EventBus) Publish(eventType EventType, data interface{}) {
	select {
	case eb.queue <- Event{Type: eventType, Data: data, Timestamp: time.Now()}:
	default:
		fmt.Println("Event queue full, dropping event:", eventType)
	}
}

// Start processing events
func (eb *EventBus) Start() {
	go func() {
		for {
			select {
			case <-eb.ctx.Done():
				return
			case event := <-eb.queue:
				eb.process(event)
			}
		}
	}()
}

func (eb *EventBus) Stop() {
	eb.cancel()
}

func (eb *EventBus) process(event Event) {
	eb.mu.RLock()
	handlers := eb.handlers[event.Type]
	eb.mu.RUnlock()

	for _, handler := range handlers {
		go func(h EventHandler) {
			if err := h(eb.ctx, event); err != nil {
				fmt.Printf("Error handling event %s: %v\n", event.Type, err)
			}
		}(handler)
	}
}
