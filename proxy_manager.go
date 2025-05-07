package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sync"
	"time"
)

// ProxyJSON представляет структуру прокси в JSON-файле
type ProxyJSON struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
	Pass string `json:"pass"`
}

// Proxy представляет информацию о прокси
type Proxy struct {
	URL        string    // Полный URL прокси (формируется из host, port, user, pass)
	Host       string    // Хост прокси
	Port       int       // Порт прокси
	User       string    // Имя пользователя для аутентификации (может быть пустым)
	Pass       string    // Пароль для аутентификации (может быть пустым)
	Weight     float64   // Вес для взвешенной ротации
	ErrorCount int       // Счетчик ошибок
	LastUsed   time.Time // Время последнего использования
	UsageCount int       // Счетчик использований
}

// ProxyManager управляет списком прокси
type ProxyManager struct {
	proxies []*Proxy     // Список прокси
	mu      sync.RWMutex // Мьютекс для синхронизации
	config  *Config      // Конфигурация
}

// NewProxyManager создает новый менеджер прокси
func NewProxyManager(config *Config) (*ProxyManager, error) {
	// Читаем список прокси из файла
	proxies, err := loadProxiesFromFile(config.ProxiesFile)
	if err != nil {
		return nil, fmt.Errorf("ошибка при загрузке прокси: %v", err)
	}

	pm := &ProxyManager{
		proxies: proxies,
		config:  config,
	}

	return pm, nil
}

// GetProxyWithoutCheck возвращает прокси без проверки его активности
func (pm *ProxyManager) GetProxyWithoutCheck() *Proxy {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if len(pm.proxies) == 0 {
		return nil
	}

	// Используем взвешенную ротацию по алгоритму Round Robin
	// с учетом времени последнего использования
	now := time.Now()
	var selectedProxy *Proxy

	// Пробуем найти прокси, который не использовался дольше всего
	oldestUsedTime := now
	for _, p := range pm.proxies {
		if p.LastUsed.IsZero() || p.LastUsed.Before(oldestUsedTime) {
			oldestUsedTime = p.LastUsed
			selectedProxy = p
		}
	}

	// Если все прокси использовались недавно, просто берем следующий по очереди
	if selectedProxy == nil {
		selectedProxy = pm.proxies[rand.Intn(len(pm.proxies))]
	}

	// Обновляем статистику прокси
	selectedProxy.LastUsed = now
	selectedProxy.UsageCount++

	return selectedProxy
}

// IncrementProxyErrorCount увеличивает счетчик ошибок прокси
func (pm *ProxyManager) IncrementProxyErrorCount(proxyURL string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, p := range pm.proxies {
		if p.URL == proxyURL {
			p.ErrorCount++
			break
		}
	}
}

// GetTotalProxiesCount возвращает общее количество прокси
func (pm *ProxyManager) GetTotalProxiesCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	return len(pm.proxies)
}

// GetProxiesStats возвращает статистику по всем прокси
func (pm *ProxyManager) GetProxiesStats() []map[string]interface{} {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	stats := make([]map[string]interface{}, 0, len(pm.proxies))
	for _, p := range pm.proxies {
		stats = append(stats, map[string]interface{}{
			"host":        p.Host,
			"port":        p.Port,
			"usage_count": p.UsageCount,
			"error_count": p.ErrorCount,
			"last_used":   p.LastUsed,
		})
	}

	return stats
}

// loadProxiesFromFile загружает список прокси из JSON-файла
func loadProxiesFromFile(filename string) ([]*Proxy, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Читаем JSON-файл
	var proxyJSONList []ProxyJSON
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&proxyJSONList); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON: %v", err)
	}

	// Конвертируем JSON-данные в структуру Proxy
	var proxies []*Proxy
	for _, pjson := range proxyJSONList {
		// Формируем URL прокси из компонентов
		proxyURL := ""

		if pjson.User != "" && pjson.Pass != "" {
			// Если указаны логин и пароль, добавляем их в URL
			proxyURL = fmt.Sprintf("http://%s:%s@%s:%d", pjson.User, pjson.Pass, pjson.Host, pjson.Port)
		} else {
			// Если логин и пароль не указаны
			proxyURL = fmt.Sprintf("http://%s:%d", pjson.Host, pjson.Port)
		}

		proxies = append(proxies, &Proxy{
			URL:    proxyURL,
			Host:   pjson.Host,
			Port:   pjson.Port,
			User:   pjson.User,
			Pass:   pjson.Pass,
			Weight: 1.0,
		})
	}

	if len(proxies) == 0 {
		return nil, fmt.Errorf("в файле не найдено прокси")
	}

	log.Printf("Загружено %d прокси из файла %s", len(proxies), filename)
	return proxies, nil
}
