//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

func main() {
	// Загружаем .env
	if err := godotenv.Load(); err != nil {
		fmt.Println("⚠️  .env файл табылмады")
	}

	key := os.Getenv("ANTHROPIC_API_KEY")

	fmt.Println("═══════════════════════════════════════")
	fmt.Println("       ANTHROPIC API ДИАГНОСТИКА")
	fmt.Println("═══════════════════════════════════════")

	// Проверка 1: ключ вообще есть?
	if key == "" {
		fmt.Println("❌ ANTHROPIC_API_KEY табылмады!")
		fmt.Println("   → .env файлында: ANTHROPIC_API_KEY=sk-ant-api03-...")
		fmt.Println("   → немесе PowerShell: $env:ANTHROPIC_API_KEY='sk-ant-...'")
		os.Exit(1)
	}
	fmt.Printf("✅ Ключ табылды: %s\n", maskKey(key))

	// Проверка 2: формат правильный?
	if !strings.HasPrefix(key, "sk-ant-") {
		fmt.Println("❌ Қате формат! Кілт 'sk-ant-' деп басталуы керек")
		fmt.Println("   Сіздің кілтіңіз:", maskKey(key))
		fmt.Println("   → https://console.anthropic.com → API Keys → Create Key")
		os.Exit(1)
	}
	fmt.Println("✅ Кілт форматы дұрыс (sk-ant-...)")

	// Проверка 3: длина нормальная?
	if len(key) < 40 {
		fmt.Printf("❌ Кілт тым қысқа (%d символ). Толық кілтті көшіріңіз\n", len(key))
		os.Exit(1)
	}
	fmt.Printf("✅ Кілт ұзындығы: %d символ\n", len(key))

	// Проверка 4: реальный запрос к API
	fmt.Println("\n🔄 Claude API-ға сұраныс жіберіліп жатыр...")

	payload := map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 10,
		"messages": []map[string]any{
			{"role": "user", "content": "Say: OK"},
		},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("❌ Желі қатесі:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	fmt.Printf("   HTTP статус: %d\n", resp.StatusCode)

	switch resp.StatusCode {
	case 200:
		fmt.Println("✅ API кілті жұмыс істейді!")
		fmt.Println("✅ Барлығы дұрыс — тест жасауды қайталаңыз")

	case 401:
		fmt.Println("❌ 401 — API кілті жарамсыз!")
		fmt.Println()
		fmt.Println("СЕБЕПТЕР:")
		fmt.Println("  1. Кілт дұрыс емес немесе өзгертілген")
		fmt.Println("  2. Кілт жойылған (console.anthropic.com-да тексеріңіз)")
		fmt.Println("  3. .env файлында пробел/тырнақша қатесі")
		fmt.Println()
		fmt.Println("ШЕШІМ:")
		fmt.Println("  1. https://console.anthropic.com → API Keys ашыңыз")
		fmt.Println("  2. Create Key → жаңа кілт жасаңыз")
		fmt.Println("  3. .env файлына дәл осылай жазыңыз:")
		fmt.Println(`     ANTHROPIC_API_KEY=sk-ant-api03-ЖАҢА_КІЛТ`)
		fmt.Println("     (тырнақша жоқ, пробел жоқ)")
		fmt.Printf("\n   API жауабы: %s\n", string(respBody))

	case 403:
		fmt.Println("❌ 403 — Кілттің рұқсаты жоқ")
		fmt.Println("   → Anthropic кабинетінде кілт параметрлерін тексеріңіз")

	case 429:
		fmt.Println("⚠️  429 — Rate limit. Кілт жұмыс істейді бірақ лимит толды")
		fmt.Println("   → Бірнеше минуттан кейін қайталаңыз")

	default:
		fmt.Printf("⚠️  Күтпеген статус %d: %s\n", resp.StatusCode, string(respBody))
	}
}

func maskKey(key string) string {
	if len(key) <= 16 {
		return strings.Repeat("*", len(key))
	}
	return key[:12] + strings.Repeat("*", len(key)-16) + key[len(key)-4:]
}
