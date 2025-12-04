package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LMStudioEmbedding implements EmbeddingProvider using LM Studio's API
type LMStudioEmbedding struct {
	baseURL    string
	model      string
	client     *http.Client
	dimensions int
}

// NewLMStudioEmbedding creates a new LM Studio embedding provider
func NewLMStudioEmbedding(baseURL, model string) *LMStudioEmbedding {
	return &LMStudioEmbedding{
		baseURL: baseURL,
		model:   model,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		dimensions: 1536, // Default, will be updated on first call
	}
}

type embeddingRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

type embeddingResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string    `json:"object"`
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func (l *LMStudioEmbedding) Embed(text string) ([]float32, error) {
	req := embeddingRequest{
		Input: text,
		Model: l.model,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := l.client.Post(l.baseURL+"/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to call embedding API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding API error: %s - %s", resp.Status, string(respBody))
	}

	var embResp embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(embResp.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	embedding := embResp.Data[0].Embedding
	l.dimensions = len(embedding)

	return embedding, nil
}

func (l *LMStudioEmbedding) EmbedBatch(texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		emb, err := l.Embed(text)
		if err != nil {
			return nil, fmt.Errorf("failed to embed text %d: %w", i, err)
		}
		results[i] = emb
	}
	return results, nil
}

func (l *LMStudioEmbedding) Dimensions() int {
	return l.dimensions
}
