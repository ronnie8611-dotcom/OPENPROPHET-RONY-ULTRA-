package services

import (
    "bytes"
    "encoding/json"
    "log"
    "net/http"
    "os"
)

func SendTelegramAlert(message string) {
    token := os.Getenv("TELEGRAM_BOT_TOKEN")
    chatID := os.Getenv("TELEGRAM_CHAT_ID")
    
    if token == "" || chatID == "" {
        return // Si no hay config, no hace nada
    }

    url := "https://api.telegram.org/bot" + token + "/sendMessage"
    payload := map[string]string{
        "chat_id": chatID,
        "text":    message,
    }
    jsonBytes, _ := json.Marshal(payload)

    resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonBytes))
    if err != nil {
        log.Printf("[TELEGRAM ERROR] No se pudo enviar la notificación: %v", err)
        return
    }
    defer resp.Body.Close()
    log.Printf("[TELEGRAM] Notificación enviada correctamente.")
}