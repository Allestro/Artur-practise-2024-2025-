package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	yaml "gopkg.in/yaml.v2"

	"log/slog"
)

// Структура для хранения настроек из config.yaml
type Config struct {
	ApiURL             string   `yaml:"api_url"`
	APIKey             string   `yaml:"api_key"`
	TelegramBotToken   string   `yaml:"telegram_bot_token"`
	FilesPath          string   `yaml:"files_path"`
	Name               string   `yaml:"name"`
	Instructions       string   `yaml:"instructions"`
	Model              string   `yaml:"model"`
	Tools              []string `yaml:"tools"`
	MaxContextMessages int      `yaml:"max_context_messages"`
}

type UserSession struct {
	mu       sync.Mutex
	Messages []map[string]interface{}
}

var (
	config       Config
	userSessions = make(map[int64]*UserSession)
	sessionsMu   sync.RWMutex
)

// Функция для чтения конфигурационного файла
func loadConfig(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("Ошибка чтения файла конфигурации: %v", err)
	}

	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return fmt.Errorf("Ошибка разбора файла конфигурации: %v", err)
	}

	if config.MaxContextMessages <= 0 {
		config.MaxContextMessages = 10 // Значение по умолчанию, если не задано или неверно
	}

	return nil
}

type AssistantCreateRequest struct {
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	Model        string `json:"model"`
	Tools        []Tool `json:"tools"`
}

type Tool struct {
	Type string `json:"type"`
}

type AssistantCreateResponse struct {
	ID string `json:"id"`
}

type VectorStoreCreateResponse struct {
	ID string `json:"id"`
}

// Функция для создания ассистента с поддержкой File Search
func createAssistant() (string, error) {
	// Преобразование инструментов в нужный формат
	tools := []Tool{}
	for _, toolType := range config.Tools {
		tools = append(tools, Tool{Type: toolType})
	}

	requestBody := AssistantCreateRequest{
		Name:         config.Name,
		Instructions: config.Instructions,
		Model:        config.Model,
		Tools:        tools,
	}

	reqBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", config.ApiURL+"assistants", bytes.NewBuffer(reqBody))
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	// Логирование запроса
	slog.Debug("Создание ассистента: отправка запроса", "url", req.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	slog.Debug("Получен ответ при создании ассистента", "body", string(body))

	var assistantResponse AssistantCreateResponse
	if err := json.Unmarshal(body, &assistantResponse); err != nil {
		return "", err
	}

	slog.Info("Ассистент создан", "assistant_id", assistantResponse.ID)
	return assistantResponse.ID, nil
}

// Функция для загрузки файла
func uploadFile(filePath string) (string, error) {
	// Логирование чтения файла
	slog.Debug("Чтение файла для загрузки", "file_path", filePath)

	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var b bytes.Buffer
	w := multipart.NewWriter(&b)

	// Добавление файла в запрос
	fw, err := w.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", err
	}
	_, err = io.Copy(fw, file)
	if err != nil {
		return "", err
	}

	// Добавление 'purpose' в запрос
	err = w.WriteField("purpose", "assistants")
	if err != nil {
		return "", err
	}

	w.Close()

	req, err := http.NewRequest("POST", config.ApiURL+"files", &b)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	slog.Debug("Загрузка файла", "url", req.URL, "file_name", filepath.Base(filePath))

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		slog.Error("Ошибка загрузки файла", "status_code", resp.StatusCode, "body", string(body))
		return "", fmt.Errorf("Ошибка загрузки файла: %s", string(body))
	}

	slog.Debug("Файл успешно загружен", "file_name", filepath.Base(filePath))

	// Получение file_id
	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", err
	}

	fileID, ok := response["id"].(string)
	if !ok {
		slog.Error("Не удалось получить file_id для файла", "body", string(body))
		return "", fmt.Errorf("Не удалось получить file_id для файла %s", filePath)
	}

	return fileID, nil
}

// Функция для создания Vector Store и загрузки файлов
func createVectorStoreAndUploadFiles() (string, error) {
	// Создание Vector Store
	req, err := http.NewRequest("POST", config.ApiURL+"vector_stores", nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	slog.Debug("Создание Vector Store", "url", req.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	slog.Debug("Получен ответ при создании Vector Store", "body", string(body))

	var vectorStoreResponse VectorStoreCreateResponse
	if err := json.Unmarshal(body, &vectorStoreResponse); err != nil {
		return "", err
	}

	vectorStoreID := vectorStoreResponse.ID
	slog.Info("Vector Store создан", "vector_store_id", vectorStoreID)

	// Загрузка файлов из директории, указанной в конфиге
	files, err := os.ReadDir(config.FilesPath)
	if err != nil {
		return "", err
	}

	for _, file := range files {
		if !file.IsDir() {
			filePath := filepath.Join(config.FilesPath, file.Name())

			// Получение file_id
			fileID, err := uploadFile(filePath)
			if err != nil {
				slog.Error("Ошибка загрузки файла", "file_name", file.Name(), "error", err)
				continue
			}

			// Регистрация файла в Vector Store
			if err := registerFileInVectorStore(vectorStoreID, fileID); err != nil {
				slog.Error("Ошибка регистрации файла в Vector Store", "file_name", file.Name(), "error", err)
				continue
			}
		}
	}

	return vectorStoreID, nil
}

// Функция для регистрации файла в Vector Store
func registerFileInVectorStore(vectorStoreID, fileID string) error {
	requestBody := map[string]string{
		"file_id": fileID,
	}

	reqBody, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("Ошибка формирования тела запроса для регистрации файла: %v", err)
	}

	req, err := http.NewRequest("POST", config.ApiURL+"vector_stores/"+vectorStoreID+"/files", bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	slog.Debug("Регистрация файла в Vector Store", "vector_store_id", vectorStoreID, "file_id", fileID)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		slog.Error("Ошибка регистрации файла", "status_code", resp.StatusCode, "body", string(body))
		return fmt.Errorf("Ошибка регистрации файла: %s", string(body))
	}

	slog.Info("Файл успешно зарегистрирован в Vector Store", "file_id", fileID)
	return nil
}

// Функция для обновления ассистента с Vector Store
func updateAssistantWithVectorStore(assistantID, vectorStoreID string) error {
	updateBody := map[string]interface{}{
		"tool_resources": map[string]interface{}{
			"file_search": map[string]interface{}{
				"vector_store_ids": []string{vectorStoreID},
			},
		},
	}

	reqBody, err := json.Marshal(updateBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", config.ApiURL+"assistants/"+assistantID, bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	slog.Debug("Обновление ассистента", "assistant_id", assistantID)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		slog.Error("Ошибка обновления ассистента", "status_code", resp.StatusCode, "body", string(body))
		return fmt.Errorf("Ошибка обновления ассистента: %s", string(body))
	}

	slog.Info("Ассистент успешно обновлен", "assistant_id", assistantID)
	return nil
}

func listenToSSEStream(resp *http.Response) (string, error) {
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var finalMessage string

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("Ошибка чтения события: %v", err)
		}

		line = strings.TrimSpace(line)
		if len(line) == 0 || !strings.HasPrefix(line, "data: ") {
			continue
		}

		eventData := line[6:]

		if eventData == "[DONE]" {
			slog.Debug("Ответ полностью получен")
			break
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(eventData), &event); err != nil {
			slog.Error("Ошибка разбора события", "error", err)
			continue
		}

		obj, ok := getString(event, "object")
		if !ok {
			continue
		}

		switch obj {
		case "thread.message.delta":
			delta, ok := getMap(event, "delta")
			if !ok {
				continue
			}
			content, ok := getArray(delta, "content")
			if !ok {
				continue
			}
			for _, part := range content {
				textPart, ok := part.(map[string]interface{})
				if !ok {
					continue
				}
				text, ok := getMap(textPart, "text")
				if !ok {
					continue
				}
				value, ok := getString(text, "value")
				if !ok {
					continue
				}
				finalMessage += value
			}
		case "thread.message.completed":
			slog.Debug("Сообщение ассистента завершено")
			break
		}
	}

	slog.Debug("Собранное сообщение от ассистента", "message", finalMessage)

	if finalMessage == "" {
		return "", fmt.Errorf("Пустой ответ от ассистента")
	}

	return finalMessage, nil
}

// Создаёт поток и запускает ассистента с обработкой SSE
func createAndRunAssistantWithStreaming(assistantID string, messages []map[string]interface{}, vectorStoreID string) (string, error) {
	requestBody := map[string]interface{}{
		"assistant_id": assistantID,
		"thread": map[string]interface{}{
			"messages": messages,
		},
		"tool_resources": map[string]interface{}{
			"file_search": map[string]interface{}{
				"vector_store_ids": []string{vectorStoreID},
			},
		},
		"temperature": 1.0,
		"top_p":       1.0,
		"stream":      true, // Активация потока
	}

	reqBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("Ошибка создания тела запроса: %v", err)
	}

	req, err := http.NewRequest("POST", config.ApiURL+"threads/runs", bytes.NewBuffer(reqBody))
	if err != nil {
		return "", fmt.Errorf("Ошибка создания HTTP-запроса: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	slog.Debug("Отправка запроса к ассистенту", "assistant_id", assistantID)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Ошибка выполнения HTTP-запроса: %v", err)
	}

	return listenToSSEStream(resp)
}

// Обрабатывает запросы Telegram и передает их ассистенту
func handleTelegramUpdates(bot *tgbotapi.BotAPI, assistantID, vectorStoreID string) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil && update.Message.Text != "" {
			userID := update.Message.From.ID
			query := update.Message.Text
			slog.Info("Получен запрос от пользователя", "user_id", userID, "query", query)

			// Обновление истории сообщений с пользователем
			sessionsMu.RLock()
			session, exists := userSessions[userID]
			sessionsMu.RUnlock()

			if !exists {
				session = &UserSession{Messages: []map[string]interface{}{}}
				sessionsMu.Lock()
				userSessions[userID] = session
				sessionsMu.Unlock()
			}

			// Добавление пользователя в историю с блокировкой
			session.mu.Lock()
			session.Messages = append(session.Messages, map[string]interface{}{
				"role":    "user",
				"content": query,
			})

			// Установка ограничения количества сообщений в истории
			if len(session.Messages) > config.MaxContextMessages {
				session.Messages = session.Messages[len(session.Messages)-config.MaxContextMessages:]
			}
			session.mu.Unlock()

			// Обработка каждого запроса в отдельной горутине (Горутина (goroutine) — это функция, выполняющаяся конкурентно с другими горутинами в том же адресном пространстве.)
			go func(update tgbotapi.Update, userID int64, session *UserSession) {
				// Копируем историю сообщений с блокировкой
				session.mu.Lock()
				messagesCopy := make([]map[string]interface{}, len(session.Messages))
				copy(messagesCopy, session.Messages)
				session.mu.Unlock()

				responseContent, err := createAndRunAssistantWithStreaming(assistantID, messagesCopy, vectorStoreID)
				if err != nil {
					slog.Error("Ошибка выполнения запроса ассистентом", "error", err)
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Ошибка обработки запроса.")
					bot.Send(msg)
					return
				}

				if responseContent == "" {
					slog.Error("Получен пустой ответ от ассистента")
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Ассистент не смог предоставить ответ.")
					bot.Send(msg)
					return
				}

				// Добавление ответа ассистента в историю с блокировкой
				session.mu.Lock()
				session.Messages = append(session.Messages, map[string]interface{}{
					"role":    "assistant",
					"content": responseContent,
				})

				if len(session.Messages) > config.MaxContextMessages {
					session.Messages = session.Messages[len(session.Messages)-config.MaxContextMessages:]
				}
				session.mu.Unlock()

				msg := tgbotapi.NewMessage(update.Message.Chat.ID, responseContent)
				bot.Send(msg)
				slog.Info("Ответ отправлен пользователю", "user_id", userID)

			}(update, userID, session)
		}
	}
}

func main() {
	// Настройка логгера
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(handler))

	// Установка конфигурации
	err := loadConfig("config.yaml")
	if err != nil {
		slog.Error("Ошибка загрузки конфигурации", "error", err)
		os.Exit(1)
	}

	// Инициализация Telegram Bot
	bot, err := tgbotapi.NewBotAPI(config.TelegramBotToken)
	if err != nil {
		slog.Error("Ошибка инициализации Telegram бота", "error", err)
		os.Exit(1)
	}
	bot.Debug = false
	slog.Info("Telegram бот авторизован", "username", bot.Self.UserName)

	// Создание ассистента
	assistantID, err := createAssistant()
	if err != nil {
		slog.Error("Ошибка создания ассистента", "error", err)
		os.Exit(1)
	}

	// Создание Vector Store и загрузка файлов
	vectorStoreID, err := createVectorStoreAndUploadFiles()
	if err != nil {
		slog.Error("Ошибка создания Vector Store и загрузки файлов", "error", err)
		os.Exit(1)
	}

	// Привязка Vector Store к ассистенту
	if err := updateAssistantWithVectorStore(assistantID, vectorStoreID); err != nil {
		slog.Error("Ошибка обновления ассистента", "error", err)
		os.Exit(1)
	}

	slog.Info("Ассистент готов к работе", "assistant_id", assistantID)

	// Обработка запросов от Telegram пользователей
	handleTelegramUpdates(bot, assistantID, vectorStoreID)
}

// Вспомогательные функции для получения значений
// getString извлекает строковое значение из map по заданному ключу.
// Возвращает строку и bool значение, указывающее, удалось ли получить строку.
func getString(m map[string]interface{}, key string) (string, bool) {
	if val, ok := m[key]; ok {
		str, ok := val.(string)
		return str, ok
	}
	return "", false
}

// getMap извлекает значение типа map[string]interface{} из map по заданному ключу.
// Возвращает карту и bool значение, указывающее, удалось ли получить карту.
func getMap(m map[string]interface{}, key string) (map[string]interface{}, bool) {
	if val, ok := m[key]; ok {
		m2, ok := val.(map[string]interface{})
		return m2, ok
	}
	return nil, false
}

// getArray извлекает срез значений типа interface{} из map по заданному ключу.
// Возвращает срез и bool значение, указывающее, удалось ли получить срез.
func getArray(m map[string]interface{}, key string) ([]interface{}, bool) {
	if val, ok := m[key]; ok {
		arr, ok := val.([]interface{})
		return arr, ok
	}
	return nil, false
}
