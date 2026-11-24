package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"prophet-trader/interfaces"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type AnthropicClient struct {
	client *anthropic.Client
	model  string
}

func NewAnthropicClient() (*AnthropicClient, error) {
	// Leer la clave. Acepta tanto ANTHROPIC_API_KEY como ANTHROPIC_AUTH_TOKEN (DeepSeek)
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_AUTH_TOKEN")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("ni ANTHROPIC_API_KEY ni ANTHROPIC_AUTH_TOKEN están configuradas en el entorno")
	}

	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)

	// Si la URL base está configurada (para DeepSeek), la aplicamos
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL != "" {
		client = anthropic.NewClient(
			option.WithAPIKey(apiKey),
			option.WithBaseURL(baseURL),
		)
	}

	model := os.Getenv("LLM_MODEL")
	if model == "" {
		model = "claude-opus-4-8"
	}

	return &AnthropicClient{
		client: &client,
		model:  model,
	}, nil
}

func (ac *AnthropicClient) GetName() string {
	return "Anthropic/DeepSeek Client"
}

func (ac *AnthropicClient) GenerateResponse(ctx context.Context, messages []interfaces.LLMMessage, tools []interfaces.LLMTool) (*interfaces.LLMResponse, error) {
	// Protección contra punteros nulos
	if ac == nil || ac.client == nil {
		return nil, fmt.Errorf("el cliente de Anthropic/DeepSeek no está inicializado correctamente")
	}

	var anthropicMessages []anthropic.MessageParam
	var systemPrompt string

	var toolResultBlocks []anthropic.ContentBlockParamUnion

	for _, msg := range messages {
		if msg.Role == "system" {
			systemPrompt = msg.Content
		} else if msg.ToolResultID != "" {
			toolResultBlocks = append(toolResultBlocks, anthropic.NewToolResultBlock(msg.ToolResultID, msg.Content, false))
		} else {
			if len(toolResultBlocks) > 0 {
				anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(toolResultBlocks...))
				toolResultBlocks = nil
			}

			if msg.Role == "assistant" {
				var blocks []anthropic.ContentBlockParamUnion
				if msg.Content != "" {
					blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
				}
				for _, tc := range msg.ToolCalls {
					var args map[string]interface{}
					if len(tc.Arguments) > 0 {
						json.Unmarshal(tc.Arguments, &args)
					}
					if args == nil {
						args = map[string]interface{}{}
					}
					blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, args, tc.Name))
				}
				if len(blocks) > 0 {
					anthropicMessages = append(anthropicMessages, anthropic.NewAssistantMessage(blocks...))
				}
			} else {
				if msg.Content != "" {
					anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content)))
				}
			}
		}
	}
	
	if len(toolResultBlocks) > 0 {
		anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(toolResultBlocks...))
	}

	var anthropicTools []anthropic.ToolUnionParam
	for _, tool := range tools {
		schemaMap := tool.InputSchema
		if schemaMap == nil {
			schemaMap = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		
		anthropicTools = append(anthropicTools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        tool.Name,
				Description: anthropic.String(tool.Description),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: schemaMap["properties"],
				},
			},
		})
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(ac.model),
		MaxTokens: 4096,
		Messages:  anthropicMessages,
	}

	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{{
			Text:         systemPrompt,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}}
	}

	if len(anthropicTools) > 0 {
		params.Tools = anthropicTools
	}

	resp, err := ac.client.Messages.New(ctx, params)
	if err != nil {
		return nil, err
	}
	
	// Protección por si la API devuelve una respuesta vacía
	if resp == nil {
		return nil, fmt.Errorf("respuesta nula recibida de la API")
	}

	var content string
	var toolCalls []interfaces.LLMToolCall

	for _, block := range resp.Content {
		if block.Type == "text" {
			content += block.Text
		} else if block.Type == "tool_use" {
			argsBytes, _ := json.Marshal(block.Input)
			toolCalls = append(toolCalls, interfaces.LLMToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: argsBytes,
			})
		}
	}

	return &interfaces.LLMResponse{
		Content:    content,
		ToolCalls:  toolCalls,
		UsageToken: int(resp.Usage.InputTokens + resp.Usage.OutputTokens),
	}, nil
}