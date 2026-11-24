package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"prophet-trader/interfaces"
)

type MultiLLMClient struct {
	providers []interfaces.LLMProvider
}

func NewMultiLLMClient(providers ...interfaces.LLMProvider) *MultiLLMClient {
	var valid []interfaces.LLMProvider
	for _, p := range providers {
		if p != nil {
			valid = append(valid, p)
		}
	}
	return &MultiLLMClient{providers: valid}
}

func (m *MultiLLMClient) GetName() string { return "MultiLLM" }

func (m *MultiLLMClient) GenerateResponse(ctx context.Context, messages []interfaces.LLMMessage, tools []interfaces.LLMTool) (*interfaces.LLMResponse, error) {
	var lastErr error
	for i, p := range m.providers {
		resp, err := p.GenerateResponse(ctx, messages, tools)
		if err == nil && resp != nil && (resp.Content != "" || len(resp.ToolCalls) > 0) {
			return resp, nil
		}
		
		if err != nil {
			lastErr = err
			fmt.Printf("[MULTI_LLM] Proveedor %d (%s) falló: %v. Intentando siguiente...\n", i+1, p.GetName(), err)
		} else {
			lastErr = fmt.Errorf("respuesta vacía del proveedor")
			fmt.Printf("[MULTI_LLM] Proveedor %d (%s) devolvió respuesta vacía. Intentando siguiente...\n", i+1, p.GetName())
		}
	}
	return nil, fmt.Errorf("todos los proveedores fallaron. Último error: %v", lastErr)
}

// FallbackClaudeClient (Plan C) con soporte para herramientas
type FallbackClaudeClient struct {
	apiKey string
	model  string
}

func NewFallbackClaudeClient() (*FallbackClaudeClient, error) {
	apiKey := os.Getenv("CLAUDE_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("CLAUDE_API_KEY no configurada")
	}
	return &FallbackClaudeClient{apiKey: apiKey, model: "claude-3-haiku-20240307"}, nil
}

func (c *FallbackClaudeClient) GetName() string { return "Claude" }

type ClaudeMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type ClaudeTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

func (c *FallbackClaudeClient) GenerateResponse(ctx context.Context, messages []interfaces.LLMMessage, tools []interfaces.LLMTool) (*interfaces.LLMResponse, error) {
	var systemPrompt string
	var claudeMessages []ClaudeMessage

	for _, m := range messages {
		if m.Role == "system" {
			systemPrompt = m.Content
		} else if m.Role == "user" && m.ToolResultID != "" {
			// Manejo de respuestas de herramientas (simplificado)
			claudeMessages = append(claudeMessages, ClaudeMessage{
				Role: "user",
				Content: []map[string]interface{}{
					{"type": "tool_result", "tool_use_id": m.ToolResultID, "content": m.Content},
				},
			})
		} else if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			// Mensaje del asistente pidiendo usar una herramienta
			var content []map[string]interface{}
			if m.Content != "" {
				content = append(content, map[string]interface{}{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				var input map[string]interface{}
				json.Unmarshal(tc.Arguments, &input)
				content = append(content, map[string]interface{}{
					"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": input,
				})
			}
			claudeMessages = append(claudeMessages, ClaudeMessage{Role: "assistant", Content: content})
		} else {
			claudeMessages = append(claudeMessages, ClaudeMessage{Role: m.Role, Content: m.Content})
		}
	}

	payload := map[string]interface{}{
		"model":      c.model,
		"max_tokens": 4096,
		"messages":   claudeMessages,
	}
	if systemPrompt != "" {
		payload["system"] = systemPrompt
	}

	if len(tools) > 0 {
		var claudeTools []ClaudeTool
		for _, t := range tools {
			claudeTools = append(claudeTools, ClaudeTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
		}
		payload["tools"] = claudeTools
	}

	jsonData, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil { return nil, err }
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("claude error: %d - %s", resp.StatusCode, string(body))
	}

	body, _ := io.ReadAll(resp.Body)
	var claudeResp struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	json.Unmarshal(body, &claudeResp)

	llmResp := &interfaces.LLMResponse{}
	for _, block := range claudeResp.Content {
		if block.Type == "text" {
			llmResp.Content += block.Text
		} else if block.Type == "tool_use" {
			llmResp.ToolCalls = append(llmResp.ToolCalls, interfaces.LLMToolCall{
				ID: block.ID, Name: block.Name, Arguments: block.Input,
			})
		}
	}
	return llmResp, nil
}

// FallbackGeminiClient (Plan B)
type FallbackGeminiClient struct {
	apiKey string
	model  string
}

func NewFallbackGeminiClient() (*FallbackGeminiClient, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" { return nil, fmt.Errorf("GEMINI_API_KEY no configurada") }
	return &FallbackGeminiClient{apiKey: apiKey, model: "gemini-3.8-flash"}, nil
}

func (g *FallbackGeminiClient) GetName() string { return "Gemini" }

func (g *FallbackGeminiClient) GenerateResponse(ctx context.Context, messages []interfaces.LLMMessage, tools []interfaces.LLMTool) (*interfaces.LLMResponse, error) {
	var prompt string
	for _, m := range messages {
		prompt += m.Role + ": " + m.Content + "\n"
	}
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", g.model, g.apiKey)
	payload := map[string]interface{}{
		"contents": []map[string]interface{}{{"parts": []map[string]string{{"text": prompt}}}},
	}
	jsonData, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil { return nil, err }
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini error: %d - %s", resp.StatusCode, string(body))
	}
	body, _ := io.ReadAll(resp.Body)
	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct { Text string `json:"text"` } `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	json.Unmarshal(body, &geminiResp)
	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("sin respuesta de Gemini")
	}
	return &interfaces.LLMResponse{Content: geminiResp.Candidates[0].Content.Parts[0].Text}, nil
}