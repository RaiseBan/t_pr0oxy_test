package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
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
	config       *Config         // Конфигурация
	proxyManager *ProxyManager   // Менеджер прокси
	metrics      *Metrics        // Метрики
	transport    *http.Transport // HTTP транспорт для повторного использования соединений
}

// NewProxyServer создает новый прокси сервер
func NewProxyServer(config *Config, pm *ProxyManager, metrics *Metrics) *ProxyServer {
	// Настраиваем транспорт с оптимизированными параметрами
	transport := &http.Transport{
		MaxIdleConns:        config.MaxIdleConns,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     60 * time.Second,
		DisableCompression:  true,  // Отключаем сжатие для повышения производительности
		DisableKeepAlives:   false, // Включаем Keep-Alive
	}

	return &ProxyServer{
		config:       config,
		proxyManager: pm,
		metrics:      metrics,
		transport:    transport,
	}
}

// Start запускает прокси сервер
func (ps *ProxyServer) Start() error {
	// Настраиваем HTTP-сервер
	server := &http.Server{
		Addr:    ps.config.ListenAddr,
		Handler: http.HandlerFunc(ps.handleRequest),
	}

	fmt.Printf("Прокси сервер запущен на %s\n", ps.config.ListenAddr)
	fmt.Println("Доступные эндпоинты:")
	for name, url := range ENDPOINTS {
		fmt.Printf(" - %s -> %s\n", name, url)
	}

	return server.ListenAndServe()
}

// handleRequest обрабатывает входящие HTTP запросы
func (ps *ProxyServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	ps.metrics.IncrementTotalRequests()
	ps.metrics.IncrementActiveConnections()
	defer ps.metrics.DecrementActiveConnections()

	// Обработка специальных эндпоинтов
	if r.URL.Path == "/health" {
		ps.handleHealthCheck(w, r)
		return
	}

	if r.URL.Path == "/favicon.ico" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

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
	// Удаляем начальный слеш
	trimmedPath := strings.TrimPrefix(path, "/")

	// Разделяем путь на компоненты
	components := strings.SplitN(trimmedPath, "/", 2)
	if len(components) == 0 {
		return "", fmt.Errorf("Некорректный путь запроса")
	}

	// Проверяем, является ли первый компонент одним из наших эндпоинтов
	endpointKey := components[0]
	endpoint, exists := ENDPOINTS[endpointKey]

	if !exists {
		return "", fmt.Errorf("Неизвестный эндпоинт: %s", endpointKey)
	}

	// Формируем целевой URL
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
	// Обратите внимание: мы НЕ проверяем прокси здесь
	response := map[string]interface{}{
		"status":         "ok",
		"active_proxies": ps.proxyManager.GetTotalProxiesCount(), // считаем все прокси активными
		"total_proxies":  ps.proxyManager.GetTotalProxiesCount(),
		"endpoints":      make([]string, 0, len(ENDPOINTS)),
	}

	// Добавляем список доступных эндпоинтов
	for name := range ENDPOINTS {
		response["endpoints"] = append(response["endpoints"].([]string), name)
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"%s","active_proxies":%d,"total_proxies":%d}`,
		response["status"], response["active_proxies"], response["total_proxies"])
}

// handleHTTP обрабатывает HTTP запросы
func (ps *ProxyServer) handleHTTP(w http.ResponseWriter, r *http.Request) {
	// Получаем прокси из менеджера (без проверки активности)
	proxy := ps.proxyManager.GetProxyWithoutCheck()
	if proxy == nil {
		ps.metrics.IncrementFailedRequests()
		http.Error(w, "Нет доступных прокси", http.StatusServiceUnavailable)
		return
	}

	// Создаем новый запрос
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

	// Настраиваем клиент с прокси
	proxyURL, _ := url.Parse(proxy.URL)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:               http.ProxyURL(proxyURL),
			MaxIdleConns:        ps.config.MaxIdleConns,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     60 * time.Second,
		},
		Timeout: time.Duration(ps.config.Timeout) * time.Second,
	}

	// Выполняем запрос
	startTime := time.Now()
	resp, err := client.Do(outReq)
	requestDuration := time.Since(startTime)

	if err != nil {
		ps.metrics.IncrementFailedRequests()
		// Не помечаем прокси как неактивный - только увеличиваем счетчик ошибок
		ps.proxyManager.IncrementProxyErrorCount(proxy.URL)
		http.Error(w, fmt.Sprintf("Ошибка запроса: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Обновляем метрики
	ps.metrics.IncrementSuccessfulRequests()
	ps.metrics.RecordResponseTime(requestDuration)

	// Копируем статус и заголовки ответа
	for name, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Копируем тело ответа
	io.Copy(w, resp.Body)
}

// handleTunneling обрабатывает HTTPS запросы через туннелирование
func (ps *ProxyServer) handleTunneling(w http.ResponseWriter, r *http.Request) {
	// Получаем прокси из менеджера без проверки активности
	proxy := ps.proxyManager.GetProxyWithoutCheck()
	if proxy == nil {
		ps.metrics.IncrementFailedRequests()
		http.Error(w, "Нет доступных прокси", http.StatusServiceUnavailable)
		return
	}

	// Разбираем URL прокси
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		ps.metrics.IncrementFailedRequests()
		http.Error(w, fmt.Sprintf("Ошибка разбора URL прокси: %v", err), http.StatusInternalServerError)
		return
	}

	// Устанавливаем соединение с прокси
	proxyConn, err := net.DialTimeout("tcp", proxyURL.Host, time.Duration(ps.config.Timeout)*time.Second)
	if err != nil {
		ps.metrics.IncrementFailedRequests()
		// Увеличиваем счетчик ошибок, но не отключаем прокси
		ps.proxyManager.IncrementProxyErrorCount(proxy.URL)
		http.Error(w, fmt.Sprintf("Ошибка соединения с прокси: %v", err), http.StatusBadGateway)
		return
	}

	// Если прокси использует HTTP Basic Auth
	auth := ""
	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		auth = fmt.Sprintf("Proxy-Authorization: Basic %s\r\n", basicAuth(username, password))
	}

	// Отправляем CONNECT запрос прокси серверу
	connectReq := fmt.Sprintf(
		"CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n",
		r.Host, r.Host, auth,
	)
	fmt.Fprint(proxyConn, connectReq)

	// Читаем ответ от прокси
	buffer := make([]byte, 1024)
	proxyConn.SetReadDeadline(time.Now().Add(time.Duration(ps.config.Timeout) * time.Second))
	n, err := proxyConn.Read(buffer)
	if err != nil {
		ps.metrics.IncrementFailedRequests()
		// Увеличиваем счетчик ошибок, но не отключаем прокси
		ps.proxyManager.IncrementProxyErrorCount(proxy.URL)
		http.Error(w, fmt.Sprintf("Ошибка чтения ответа от прокси: %v", err), http.StatusBadGateway)
		return
	}

	// Проверяем, что CONNECT запрос успешен
	response := string(buffer[:n])
	if !strings.Contains(response, "200") {
		ps.metrics.IncrementFailedRequests()
		// Увеличиваем счетчик ошибок, но не отключаем прокси
		ps.proxyManager.IncrementProxyErrorCount(proxy.URL)
		http.Error(w, "Ошибка установки туннеля через прокси", http.StatusBadGateway)
		return
	}

	// Отправляем клиенту HTTP 200 OK
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

	// Теперь у нас есть два соединения: клиент <-> прокси сервер, прокси сервер <-> целевой сервер
	// Просто передаем данные между ними
	ps.metrics.IncrementSuccessfulRequests()

	go transfer(clientConn, proxyConn)
	go transfer(proxyConn, clientConn)
}

// transfer копирует данные между двумя соединениями
func transfer(dst, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	io.Copy(dst, src)
}

// basicAuth кодирует логин и пароль для Basic Authentication
func basicAuth(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
}
