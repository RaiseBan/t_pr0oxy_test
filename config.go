package main

import (
	"encoding/json"
	"os"
)

// Config содержит настройки прокси сервера
type Config struct {
	ListenAddr    string `json:"listen_addr"`    // Адрес для прослушивания
	ProxiesFile   string `json:"proxies_file"`   // Файл со списком прокси в JSON формате
	Timeout       int    `json:"timeout"`        // Таймаут в секундах
	WorkerCount   int    `json:"worker_count"`   // Количество воркеров
	MetricsAddr   string `json:"metrics_addr"`   // Адрес для метрик
	CheckInterval int    `json:"check_interval"` // Интервал проверки прокси (сек)
	MaxIdleConns  int    `json:"max_idle_conns"` // Максимальное количество простаивающих соединений
}

// LoadConfig загружает конфигурацию из файла
func LoadConfig(filename string) (*Config, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var config Config
	if err := json.NewDecoder(file).Decode(&config); err != nil {
		return nil, err
	}

	// Устанавливаем значения по умолчанию, если они не указаны в конфиге
	if config.ListenAddr == "" {
		config.ListenAddr = ":8082"
	}
	if config.ProxiesFile == "" {
		config.ProxiesFile = "proxies.json"
	}
	if config.Timeout == 0 {
		config.Timeout = 10
	}
	if config.WorkerCount == 0 {
		config.WorkerCount = 2000 // Увеличено для максимальной производительности
	}
	if config.MetricsAddr == "" {
		config.MetricsAddr = ":9090"
	}
	if config.CheckInterval == 0 {
		config.CheckInterval = 30
	}
	if config.MaxIdleConns == 0 {
		config.MaxIdleConns = 10000 // Увеличено для максимальной производительности
	}

	return &config, nil
}
