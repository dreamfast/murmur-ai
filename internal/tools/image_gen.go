package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// imageGenTimeout is the maximum time to wait for image generation.
const imageGenTimeout = 5 * time.Minute

// imageGenPollInterval is how often to poll for generation completion.
const imageGenPollInterval = 5 * time.Second

// defaultCheckpointName is the default SDXL model checkpoint filename.
const defaultCheckpointName = "sd_xl_base_1.0.safetensors"

// safeFilenameRe matches characters allowed in sanitized filename components.
var safeFilenameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// ImageGenToolConfig holds configuration for the image_gen tool.
type ImageGenToolConfig struct {
	// ComfyUIHost is the base URL of the ComfyUI API.
	ComfyUIHost string
	// OutputDir is the local directory for saving generated images.
	OutputDir string
	// UploadURL is an optional URL to upload images for sharing.
	UploadURL string
	// CheckpointName is the model checkpoint filename for ComfyUI.
	// Defaults to "sd_xl_base_1.0.safetensors" if empty.
	CheckpointName string
	// HTTPClient is an optional HTTP client for testing.
	HTTPClient *http.Client
}

// NewImageGenTool creates the image_gen tool for generating images via ComfyUI.
func NewImageGenTool(cfg ImageGenToolConfig) Tool {
	return Tool{
		Name:        "image_gen",
		Description: "Generate images using ComfyUI (Stable Diffusion). Provide a text prompt and optional parameters for dimensions, steps, and seed.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"prompt": {
					"type": "string",
					"description": "Text prompt describing the image to generate"
				},
				"negative_prompt": {
					"type": "string",
					"description": "Negative prompt — things to avoid in the image"
				},
				"width": {
					"type": "integer",
					"description": "Image width in pixels (64-2048, divisible by 8, default 1024)"
				},
				"height": {
					"type": "integer",
					"description": "Image height in pixels (64-2048, divisible by 8, default 1024)"
				},
				"steps": {
					"type": "integer",
					"description": "Number of sampling steps (1-100, default 20)"
				},
				"seed": {
					"type": "integer",
					"description": "Random seed for reproducibility (default: random)"
				}
			},
			"required": ["prompt"]
		}`),
		Handler: newImageGenHandler(cfg),
	}
}

// newImageGenHandler returns a handler function closed over the image_gen config.
func newImageGenHandler(cfg ImageGenToolConfig) func(ctx context.Context, args map[string]any) (string, error) {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	checkpointName := cfg.CheckpointName
	if checkpointName == "" {
		checkpointName = defaultCheckpointName
	}

	return func(ctx context.Context, args map[string]any) (string, error) {
		prompt, err := RequireStringArg(args, "prompt")
		if err != nil {
			return "", err
		}

		negPrompt := OptionalStringArg(args, "negative_prompt", "")
		width := optionalIntArg(args, "width", 1024)
		height := optionalIntArg(args, "height", 1024)
		steps := optionalIntArg(args, "steps", 20)
		seed := optionalIntArg(args, "seed", -1)

		// Validate dimensions.
		if err := validateDimensions(width, height); err != nil {
			return "", err
		}

		// Validate steps.
		if steps < 1 || steps > 100 {
			return "", fmt.Errorf("steps must be between 1 and 100, got %d", steps)
		}

		// Generate random seed if not provided (-1 sentinel means "not set").
		if seed < 0 {
			seed = int(rand.Int64N(1<<53 - 1))
		}

		genCtx, cancel := context.WithTimeout(ctx, imageGenTimeout)
		defer cancel()

		// Build and submit workflow.
		workflow := buildWorkflow(prompt, negPrompt, width, height, steps, seed, checkpointName)
		promptID, err := submitWorkflow(genCtx, client, cfg.ComfyUIHost, workflow)
		if err != nil {
			return "", fmt.Errorf("image_gen: submit workflow: %w", err)
		}

		// Poll for completion.
		outputInfo, err := pollForCompletion(genCtx, client, cfg.ComfyUIHost, promptID)
		if err != nil {
			return "", fmt.Errorf("image_gen: poll: %w", err)
		}

		// Download the image.
		imageData, err := downloadImage(genCtx, client, cfg.ComfyUIHost, outputInfo)
		if err != nil {
			return "", fmt.Errorf("image_gen: download: %w", err)
		}

		// Sanitize promptID to prevent path traversal in filename.
		safeID := sanitizeFilenameComponent(promptID)

		// Ensure output directory exists.
		if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
			return "", fmt.Errorf("image_gen: create output dir: %w", err)
		}

		// Save to output directory.
		filename := fmt.Sprintf("murmur_%s_%dx%d.png", safeID, width, height)
		savePath := filepath.Join(cfg.OutputDir, filename)
		if err := os.WriteFile(savePath, imageData, 0644); err != nil {
			return "", fmt.Errorf("image_gen: save file: %w", err)
		}

		result := fmt.Sprintf("Image generated: %s (%dx%d, %d steps, seed %d)", filename, width, height, steps, seed)

		// Upload if configured.
		if cfg.UploadURL != "" {
			uploadedURL, err := uploadImage(genCtx, client, cfg.UploadURL, imageData, filename)
			if err != nil {
				result += fmt.Sprintf("\nUpload failed: %v", err)
			} else {
				result += fmt.Sprintf("\nURL: %s", uploadedURL)
			}
		}

		return result, nil
	}
}

// validateDimensions checks that width and height are valid for image generation.
func validateDimensions(width, height int) error {
	if width < 64 || width > 2048 {
		return fmt.Errorf("width must be between 64 and 2048, got %d", width)
	}
	if height < 64 || height > 2048 {
		return fmt.Errorf("height must be between 64 and 2048, got %d", height)
	}
	if width%8 != 0 {
		return fmt.Errorf("width must be divisible by 8, got %d", width)
	}
	if height%8 != 0 {
		return fmt.Errorf("height must be divisible by 8, got %d", height)
	}
	return nil
}

// sanitizeFilenameComponent removes any characters that could cause path
// traversal or other filesystem issues from a string used in a filename.
func sanitizeFilenameComponent(s string) string {
	// Remove path separators and parent directory references.
	s = strings.ReplaceAll(s, "..", "_")
	s = safeFilenameRe.ReplaceAllString(s, "_")
	if s == "" {
		s = "unknown"
	}
	return s
}

// comfyUIPromptResponse is the response from POST /prompt.
type comfyUIPromptResponse struct {
	PromptID string `json:"prompt_id"`
}

// comfyUIOutputInfo holds the filename and subfolder of a generated image.
type comfyUIOutputInfo struct {
	Filename  string
	Subfolder string
}

// buildWorkflow creates a ComfyUI workflow JSON for txt2img generation.
// The workflow uses an SDXL-compatible node graph: CheckpointLoaderSimple →
// CLIPTextEncode (positive + negative) → KSampler → VAEDecode → SaveImage.
func buildWorkflow(prompt, negPrompt string, width, height, steps, seed int, checkpointName string) map[string]any {
	return map[string]any{
		"3": map[string]any{
			"class_type": "KSampler",
			"inputs": map[string]any{
				"seed":         seed,
				"steps":        steps,
				"cfg":          7.0,
				"sampler_name": "euler",
				"scheduler":    "normal",
				"denoise":      1.0,
				"model":        []any{"4", 0},
				"positive":     []any{"6", 0},
				"negative":     []any{"7", 0},
				"latent_image": []any{"5", 0},
			},
		},
		"4": map[string]any{
			"class_type": "CheckpointLoaderSimple",
			"inputs": map[string]any{
				"ckpt_name": checkpointName,
			},
		},
		"5": map[string]any{
			"class_type": "EmptyLatentImage",
			"inputs": map[string]any{
				"width":      width,
				"height":     height,
				"batch_size": 1,
			},
		},
		"6": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs": map[string]any{
				"text": prompt,
				"clip": []any{"4", 1},
			},
		},
		"7": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs": map[string]any{
				"text": negPrompt,
				"clip": []any{"4", 1},
			},
		},
		"8": map[string]any{
			"class_type": "VAEDecode",
			"inputs": map[string]any{
				"samples": []any{"3", 0},
				"vae":     []any{"4", 2},
			},
		},
		"9": map[string]any{
			"class_type": "SaveImage",
			"inputs": map[string]any{
				"filename_prefix": "murmur",
				"images":          []any{"8", 0},
			},
		},
	}
}

// submitWorkflow sends the workflow to ComfyUI and returns the prompt ID.
func submitWorkflow(ctx context.Context, client *http.Client, host string, workflow map[string]any) (string, error) {
	body := map[string]any{
		"prompt": workflow,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal workflow: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/prompt", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("post prompt: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("comfyui returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result comfyUIPromptResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if result.PromptID == "" {
		return "", fmt.Errorf("empty prompt_id in response")
	}

	return result.PromptID, nil
}

// pollForCompletion polls the ComfyUI history endpoint until the prompt is complete.
func pollForCompletion(ctx context.Context, client *http.Client, host, promptID string) (*comfyUIOutputInfo, error) {
	historyURL := fmt.Sprintf("%s/history/%s", host, url.PathEscape(promptID))
	ticker := time.NewTicker(imageGenPollInterval)
	defer ticker.Stop()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, historyURL, nil)
		if err != nil {
			return nil, fmt.Errorf("create poll request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			// Transient error — wait and retry.
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("generation timed out: %w", ctx.Err())
			case <-ticker.C:
				continue
			}
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		resp.Body.Close()

		if err != nil || resp.StatusCode != http.StatusOK {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("generation timed out: %w", ctx.Err())
			case <-ticker.C:
				continue
			}
		}

		// Parse the history response.
		var history map[string]any
		if err := json.Unmarshal(body, &history); err != nil {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("generation timed out: %w", ctx.Err())
			case <-ticker.C:
				continue
			}
		}

		// Check if our prompt is in the history.
		promptData, ok := history[promptID]
		if !ok {
			// Not ready yet — wait for next tick.
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("generation timed out: %w", ctx.Err())
			case <-ticker.C:
				continue
			}
		}

		// Extract output info.
		info, err := extractOutputInfo(promptData)
		if err != nil {
			return nil, err
		}

		return info, nil
	}
}

// extractOutputInfo extracts the output filename from the ComfyUI history response.
func extractOutputInfo(promptData any) (*comfyUIOutputInfo, error) {
	dataMap, ok := promptData.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected prompt data type")
	}

	outputs, ok := dataMap["outputs"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("no outputs in prompt data")
	}

	// Find the SaveImage node output (node "9" in our workflow).
	for _, nodeOutput := range outputs {
		nodeMap, ok := nodeOutput.(map[string]any)
		if !ok {
			continue
		}
		images, ok := nodeMap["images"].([]any)
		if !ok || len(images) == 0 {
			continue
		}
		imgMap, ok := images[0].(map[string]any)
		if !ok {
			continue
		}
		filename, _ := imgMap["filename"].(string)
		subfolder, _ := imgMap["subfolder"].(string)
		if filename != "" {
			return &comfyUIOutputInfo{
				Filename:  filename,
				Subfolder: subfolder,
			}, nil
		}
	}

	return nil, fmt.Errorf("no output images found in prompt results")
}

// downloadImage downloads the generated image from ComfyUI.
func downloadImage(ctx context.Context, client *http.Client, host string, info *comfyUIOutputInfo) ([]byte, error) {
	q := url.Values{}
	q.Set("filename", info.Filename)
	q.Set("type", "output")
	if info.Subfolder != "" {
		q.Set("subfolder", info.Subfolder)
	}
	viewURL := fmt.Sprintf("%s/view?%s", host, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, viewURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	// Limit to 50MB for safety.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}

	return data, nil
}

// uploadImage uploads the image to the configured upload URL via raw binary POST
// with the filename in an X-Filename header.
func uploadImage(ctx context.Context, client *http.Client, uploadURL string, data []byte, filename string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Filename", filename)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("upload returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("read upload response: %w", err)
	}

	return strings.TrimSpace(string(body)), nil
}
