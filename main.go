package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Парсим флаги командной строки
	configFile := flag.String("config", "config.json", "Путь к файлу конфигурации")
	flag.Parse()

	// Загружаем конфигурацию
	config, err := LoadConfig(*configFile)
	if err != nil {
		log.Fatalf("Ошибка загрузки конфигурации: %v", err)
	}

	// Инициализируем генератор случайных чисел
	rand.Seed(time.Now().UnixNano())

	// Создаем менеджер прокси
	proxyManager, err := NewProxyManager(config)
	if err != nil {
		log.Fatalf("Ошибка создания менеджера прокси: %v", err)
	}

	// Создаем систему метрик
	metrics := NewMetrics(proxyManager)

	// Запускаем сервер метрик
	metrics.StartMetricsServer(config.MetricsAddr)

	// Создаем прокси сервер
	server := NewProxyServer(config, proxyManager, metrics)

	// Обрабатываем сигналы завершения
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)

	// Запускаем прокси сервер в отдельной горутине
	go func() {
		if err := server.Start(); err != nil {
			log.Fatalf("Ошибка запуска прокси сервера: %v", err)
		}
	}()

	fmt.Println("Прокси сервер успешно запущен")
	fmt.Printf("Прослушивание на %s, метрики доступны на %s\n", config.ListenAddr, config.MetricsAddr)

	// Ожидаем сигнала завершения
	sig := <-signalCh
	fmt.Printf("Получен сигнал %v, завершение работы...\n", sig)
}
