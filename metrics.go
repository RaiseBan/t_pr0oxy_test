package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics содержит метрики прокси сервера
type Metrics struct {
	TotalRequests      uint64        // Общее количество запросов
	SuccessfulRequests uint64        // Успешные запросы
	FailedRequests     uint64        // Неудачные запросы
	ActiveConnections  int32         // Активные соединения
	ProxyManager       *ProxyManager // Менеджер прокси
	StartTime          time.Time     // Время запуска сервера

	// Для статистики времени отклика
	responseTimes      []time.Duration // Список времен отклика
	responseTimesMutex sync.Mutex      // Мьютекс для доступа к списку
	maxResponseTimes   int             // Максимальный размер списка
}

// NewMetrics создает новый объект метрик
func NewMetrics(pm *ProxyManager) *Metrics {
	return &Metrics{
		ProxyManager:     pm,
		StartTime:        time.Now(),
		maxResponseTimes: 1000,
		responseTimes:    make([]time.Duration, 0, 1000),
	}
}

// IncrementTotalRequests увеличивает счетчик запросов
func (m *Metrics) IncrementTotalRequests() {
	atomic.AddUint64(&m.TotalRequests, 1)
}

// IncrementSuccessfulRequests увеличивает счетчик успешных запросов
func (m *Metrics) IncrementSuccessfulRequests() {
	atomic.AddUint64(&m.SuccessfulRequests, 1)
}

// IncrementFailedRequests увеличивает счетчик неудачных запросов
func (m *Metrics) IncrementFailedRequests() {
	atomic.AddUint64(&m.FailedRequests, 1)
}

// IncrementActiveConnections увеличивает счетчик активных соединений
func (m *Metrics) IncrementActiveConnections() {
	atomic.AddInt32(&m.ActiveConnections, 1)
}

// DecrementActiveConnections уменьшает счетчик активных соединений
func (m *Metrics) DecrementActiveConnections() {
	atomic.AddInt32(&m.ActiveConnections, -1)
}

// GetActiveConnections возвращает количество активных соединений
func (m *Metrics) GetActiveConnections() int32 {
	return atomic.LoadInt32(&m.ActiveConnections)
}

// RecordResponseTime записывает время ответа
func (m *Metrics) RecordResponseTime(duration time.Duration) {
	m.responseTimesMutex.Lock()
	defer m.responseTimesMutex.Unlock()

	m.responseTimes = append(m.responseTimes, duration)

	// Ограничиваем размер списка
	if len(m.responseTimes) > m.maxResponseTimes {
		m.responseTimes = m.responseTimes[1:]
	}
}

// GetAverageResponseTime возвращает среднее время ответа в миллисекундах
func (m *Metrics) GetAverageResponseTime() float64 {
	m.responseTimesMutex.Lock()
	defer m.responseTimesMutex.Unlock()

	if len(m.responseTimes) == 0 {
		return 0
	}

	var total time.Duration
	for _, d := range m.responseTimes {
		total += d
	}

	return float64(total.Milliseconds()) / float64(len(m.responseTimes))
}

// StartMetricsServer запускает HTTP-сервер для метрик
func (m *Metrics) StartMetricsServer(addr string) {
	mux := http.NewServeMux()

	// Эндпоинт для метрик в формате JSON
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		uptime := time.Since(m.StartTime)

		metrics := map[string]interface{}{
			"total_requests":      atomic.LoadUint64(&m.TotalRequests),
			"successful_requests": atomic.LoadUint64(&m.SuccessfulRequests),
			"failed_requests":     atomic.LoadUint64(&m.FailedRequests),
			"active_connections":  atomic.LoadInt32(&m.ActiveConnections),
			"total_proxies":       m.ProxyManager.GetTotalProxiesCount(),
			"uptime_seconds":      int(uptime.Seconds()),
			"uptime_human":        formatUptime(uptime),
			"requests_per_second": float64(atomic.LoadUint64(&m.TotalRequests)) / uptime.Seconds(),
			"average_response_ms": m.GetAverageResponseTime(),
		}

		// Добавляем информацию о доступных эндпоинтах
		endpoints := make([]string, 0, len(ENDPOINTS))
		for name := range ENDPOINTS {
			endpoints = append(endpoints, name)
		}
		metrics["endpoints"] = endpoints

		jsonData, err := json.MarshalIndent(metrics, "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Write(jsonData)
	})

	// Эндпоинт для информации о прокси
	mux.HandleFunc("/proxies", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		stats := m.ProxyManager.GetProxiesStats()

		jsonData, err := json.MarshalIndent(stats, "", "  ")
		if err != nil {
			http.Error(w, fmt.Sprintf("Ошибка сериализации данных прокси: %v", err), http.StatusInternalServerError)
			return
		}

		w.Write(jsonData)
	})

	// Эндпоинт для проверки работоспособности
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	// Запускаем HTTP-сервер для метрик в отдельной горутине
	go func() {
		fmt.Printf("Сервер метрик запущен на %s\n", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			fmt.Printf("Ошибка запуска сервера метрик: %v\n", err)
		}
	}()
}

// formatUptime форматирует время работы в человекочитаемом формате
func formatUptime(d time.Duration) string {
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dд %dч %dм %dс", days, hours, minutes, seconds)
	} else if hours > 0 {
		return fmt.Sprintf("%dч %dм %dс", hours, minutes, seconds)
	} else if minutes > 0 {
		return fmt.Sprintf("%dм %dс", minutes, seconds)
	}

	return fmt.Sprintf("%dс", seconds)
}
