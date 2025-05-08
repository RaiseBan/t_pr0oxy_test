package main

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Карта эндпоинтов
var ENDPOINTS = map[string]string{
	"jitoNY":        "https://ny.mainnet.block-engine.jito.wtf",
	"jitoTOKIO":     "https://tokyo.mainnet.block-engine.jito.wtf",
	"jitoSLC":       "https://slc.mainnet.block-engine.jito.wtf",
	"jitoAMSTERDAM": "https://amsterdam.mainnet.block-engine.jito.wtf",
	"jitoFRANKFURT": "https://frankfurt.mainnet.block-engine.jito.wtf",
	"jitoLONDON":    "https://london.mainnet.block-engine.jito.wtf",
}

// ProxyServer представляет HTTP-прокси сервер
type ProxyServer struct {
	config        *Config           // Конфигурация
	proxyManager  *ProxyManager     // Менеджер прокси
	metrics       *Metrics          // Метрики
	transportPool sync.Map          // Пул транспортов для каждого прокси
	requestQueue  chan *requestTask // Очередь запросов для воркеров
}

type requestTask struct {
	w    http.ResponseWriter
	r    *http.Request
	done chan bool
}

// NewProxyServer создает новый прокси сервер
func NewProxyServer(config *Config, pm *ProxyManager, metrics *Metrics) *ProxyServer {
	return &ProxyServer{
		config:       config,
		proxyManager: pm,
		metrics:      metrics,
	}
}

// startWorkers запускает пул воркеров для обработки запросов
func (ps *ProxyServer) startWorkers() {
	ps.requestQueue = make(chan *requestTask, ps.config.WorkerCount*2)

	for i := 0; i < ps.config.WorkerCount; i++ {
		go ps.worker(i)
	}
}

func (ps *ProxyServer) worker(id int) {
	for task := range ps.requestQueue {
		ps.processRequest(task.w, task.r)
		task.done <- true
	}
}

// getTransport получает или создает транспорт для прокси
func (ps *ProxyServer) getTransport(proxyURL string) *http.Transport {
	if t, ok := ps.transportPool.Load(proxyURL); ok {
		return t.(*http.Transport)
	}

	parsedURL, _ := url.Parse(proxyURL)

	transport := &http.Transport{
		Proxy:                 http.ProxyURL(parsedURL),
		MaxIdleConns:          100, // Уменьшаем для меньшей группировки
		MaxIdleConnsPerHost:   10,  // Уменьшаем
		MaxConnsPerHost:       0,   // Без ограничений
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
		DisableKeepAlives:     true,  // ВАЖНО: отключаем keep-alive для предотвращения группировки
		ForceAttemptHTTP2:     false, // Отключаем HTTP/2 для избежания мультиплексирования
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: -1, // Отключаем TCP keep-alive
			DualStack: true,
		}).DialContext,
	}

	ps.transportPool.Store(proxyURL, transport)
	return transport
}

// startTransportCleaner запускает периодическую очистку транспортов
func (ps *ProxyServer) startTransportCleaner() {
	ticker := time.NewTicker(2 * time.Minute)
	go func() {
		for range ticker.C {
			ps.transportPool.Range(func(key, value interface{}) bool {
				transport := value.(*http.Transport)
				transport.CloseIdleConnections()
				return true
			})
			log.Printf("Transport pool cleaned. Goroutines: %d", runtime.NumGoroutine())
		}
	}()
}

// Start запускает прокси сервер
func (ps *ProxyServer) Start() error {
	// Запускаем воркеров
	ps.startWorkers()

	// Запускаем периодическую очистку транспортов
	ps.startTransportCleaner()

	// Настраиваем HTTP-сервер с оптимизациями
	server := &http.Server{
		Addr:         ps.config.ListenAddr,
		Handler:      http.HandlerFunc(ps.handleRequest),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	fmt.Printf("Прокси сервер запущен на %s с %d воркерами\n", ps.config.ListenAddr, ps.config.WorkerCount)
	fmt.Println("Доступные эндпоинты:")
	for name, url := range ENDPOINTS {
		fmt.Printf(" - %s -> %s\n", name, url)
	}

	return server.ListenAndServe()
}

// handleRequest обрабатывает входящие HTTP запросы
func (ps *ProxyServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	// Специальные эндпоинты обрабатываем напрямую
	if r.URL.Path == "/health" {
		ps.handleHealthCheck(w, r)
		return
	}

	if r.URL.Path == "/favicon.ico" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Отправляем в очередь для обработки воркерами
	task := &requestTask{
		w:    w,
		r:    r,
		done: make(chan bool, 1),
	}

	select {
	case ps.requestQueue <- task:
		<-task.done
	default:
		// Очередь полна
		ps.metrics.IncrementFailedRequests()
		http.Error(w, "Server overloaded", http.StatusServiceUnavailable)
	}
}

// processRequest обрабатывает отдельный запрос
func (ps *ProxyServer) processRequest(w http.ResponseWriter, r *http.Request) {
	ps.metrics.IncrementTotalRequests()
	ps.metrics.IncrementActiveConnections()
	defer ps.metrics.DecrementActiveConnections()

	// Парсим путь для определения целевого URL
	targetURL, err := ps.parseTargetURL(r.URL.Path)
	if err != nil {
		ps.metrics.IncrementFailedRequests()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Для HTTPS-запросов используем туннелирование
	if r.Method == http.MethodConnect {
		ps.handleTunneling(w, r)
		return
	}

	// Создаем новый URL для запроса
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		ps.metrics.IncrementFailedRequests()
		http.Error(w, fmt.Sprintf("Ошибка парсинга URL: %v", err), http.StatusInternalServerError)
		return
	}

	// Перенаправляем запрос
	r.URL = parsedURL
	ps.handleHTTP(w, r)
}

// parseTargetURL извлекает целевой URL из пути запроса
func (ps *ProxyServer) parseTargetURL(path string) (string, error) {
	trimmedPath := strings.TrimPrefix(path, "/")
	components := strings.SplitN(trimmedPath, "/", 2)
	if len(components) == 0 {
		return "", fmt.Errorf("Некорректный путь запроса")
	}

	endpointKey := components[0]
	endpoint, exists := ENDPOINTS[endpointKey]

	if !exists {
		return "", fmt.Errorf("Неизвестный эндпоинт: %s", endpointKey)
	}

	var remainingPath string
	if len(components) > 1 {
		remainingPath = "/" + components[1]
	} else {
		remainingPath = "/"
	}

	return endpoint + remainingPath, nil
}

// handleHealthCheck обрабатывает запрос проверки работоспособности
func (ps *ProxyServer) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"status":         "ok",
		"active_proxies": ps.proxyManager.GetTotalProxiesCount(),
		"total_proxies":  ps.proxyManager.GetTotalProxiesCount(),
		"endpoints":      make([]string, 0, len(ENDPOINTS)),
		"workers":        ps.config.WorkerCount,
		"queue_size":     len(ps.requestQueue),
	}

	for name := range ENDPOINTS {
		response["endpoints"] = append(response["endpoints"].([]string), name)
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"%s","active_proxies":%d,"total_proxies":%d,"workers":%d,"queue_size":%d}`,
		response["status"], response["active_proxies"], response["total_proxies"],
		response["workers"], response["queue_size"])
}

// handleHTTP обрабатывает HTTP запросы
func (ps *ProxyServer) handleHTTP(w http.ResponseWriter, r *http.Request) {
	proxy := ps.proxyManager.GetProxyWithoutCheck()
	if proxy == nil {
		ps.metrics.IncrementFailedRequests()
		http.Error(w, "Нет доступных прокси", http.StatusServiceUnavailable)
		return
	}

	outReq, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
	if err != nil {
		ps.metrics.IncrementFailedRequests()
		http.Error(w, fmt.Sprintf("Ошибка создания запроса: %v", err), http.StatusInternalServerError)
		return
	}

	// Копируем заголовки
	for name, values := range r.Header {
		for _, value := range values {
			outReq.Header.Add(name, value)
		}
	}

	// Получаем транспорт из пула
	transport := ps.getTransport(proxy.URL)

	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(ps.config.Timeout) * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	startTime := time.Now()
	resp, err := client.Do(outReq)
	requestDuration := time.Since(startTime)

	if err != nil {
		ps.metrics.IncrementFailedRequests()
		ps.proxyManager.IncrementProxyErrorCount(proxy.URL)
		http.Error(w, fmt.Sprintf("Ошибка запроса: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	ps.metrics.IncrementSuccessfulRequests()
	ps.metrics.RecordResponseTime(requestDuration)

	// Копируем заголовки ответа
	for name, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Используем большой буфер для копирования
	buf := make([]byte, 256*1024) // 256KB буфер
	_, err = io.CopyBuffer(w, resp.Body, buf)
	if err != nil && err != io.EOF {
		log.Printf("Error copying response body: %v", err)
	}
}

// handleTunneling обрабатывает HTTPS запросы через туннелирование
func (ps *ProxyServer) handleTunneling(w http.ResponseWriter, r *http.Request) {
	proxy := ps.proxyManager.GetProxyWithoutCheck()
	if proxy == nil {
		ps.metrics.IncrementFailedRequests()
		http.Error(w, "Нет доступных прокси", http.StatusServiceUnavailable)
		return
	}

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		ps.metrics.IncrementFailedRequests()
		http.Error(w, fmt.Sprintf("Ошибка разбора URL прокси: %v", err), http.StatusInternalServerError)
		return
	}

	proxyConn, err := net.DialTimeout("tcp", proxyURL.Host, time.Duration(ps.config.Timeout)*time.Second)
	if err != nil {
		ps.metrics.IncrementFailedRequests()
		ps.proxyManager.IncrementProxyErrorCount(proxy.URL)
		http.Error(w, fmt.Sprintf("Ошибка соединения с прокси: %v", err), http.StatusBadGateway)
		return
	}
	defer proxyConn.Close()

	// Устанавливаем размеры буферов для TCP соединения
	if tcpConn, ok := proxyConn.(*net.TCPConn); ok {
		tcpConn.SetReadBuffer(256 * 1024)  // 256KB
		tcpConn.SetWriteBuffer(256 * 1024) // 256KB
	}

	auth := ""
	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		auth = fmt.Sprintf("Proxy-Authorization: Basic %s\r\n", basicAuth(username, password))
	}

	connectReq := fmt.Sprintf(
		"CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n",
		r.Host, r.Host, auth,
	)
	fmt.Fprint(proxyConn, connectReq)

	buffer := make([]byte, 1024)
	proxyConn.SetReadDeadline(time.Now().Add(time.Duration(ps.config.Timeout) * time.Second))
	n, err := proxyConn.Read(buffer)
	if err != nil {
		ps.metrics.IncrementFailedRequests()
		ps.proxyManager.IncrementProxyErrorCount(proxy.URL)
		http.Error(w, fmt.Sprintf("Ошибка чтения ответа от прокси: %v", err), http.StatusBadGateway)
		return
	}

	response := string(buffer[:n])
	if !strings.Contains(response, "200") {
		ps.metrics.IncrementFailedRequests()
		ps.proxyManager.IncrementProxyErrorCount(proxy.URL)
		http.Error(w, "Ошибка установки туннеля через прокси", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		ps.metrics.IncrementFailedRequests()
		http.Error(w, "Hijacking не поддерживается", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		ps.metrics.IncrementFailedRequests()
		http.Error(w, fmt.Sprintf("Ошибка hijacking: %v", err), http.StatusInternalServerError)
		return
	}

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	ps.metrics.IncrementSuccessfulRequests()

	// Используем буферизованное копирование с большими буферами
	buf1 := make([]byte, 256*1024)
	buf2 := make([]byte, 256*1024)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer clientConn.Close()
		io.CopyBuffer(clientConn, proxyConn, buf1)
	}()

	go func() {
		defer wg.Done()
		defer proxyConn.Close()
		io.CopyBuffer(proxyConn, clientConn, buf2)
	}()

	wg.Wait()
}

func basicAuth(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
}
